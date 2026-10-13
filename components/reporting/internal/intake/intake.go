// Package intake owns private Forgejo delivery. It consumes selected JSON bug
// reports only; it cannot read project memory or execute commands.
package intake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const MaxReportBytes = 32 << 10
const maxResponseBytes = 1 << 20

type Config struct {
	RepositoryURL       string   `json:"repository_url"`
	AllowedProjectIDs   []string `json:"allowed_project_ids"`
	AutomaticSubmission bool     `json:"automatic_submission"`
	TokenEnv            string   `json:"token_env"`
	Outbox              string   `json:"outbox"`
}

// Report is the v1 Continuity wire interface. Unknown fields are rejected so a
// future expansion cannot silently turn this selected report into a data dump.
type Report struct {
	SchemaVersion int    `json:"schema_version"`
	ReportID      string `json:"report_id"`
	Source        struct {
		ProjectID  string    `json:"project_id"`
		Ref        string    `json:"ref"`
		Creator    string    `json:"creator"`
		Model      string    `json:"model,omitempty"`
		Authority  string    `json:"authority"`
		RecordedAt time.Time `json:"recorded_at"`
	} `json:"source"`
	Title               string `json:"title"`
	Description         string `json:"description"`
	Observed            string `json:"observed,omitempty"`
	Expected            string `json:"expected,omitempty"`
	Reproduction        string `json:"reproduction,omitempty"`
	Environment         string `json:"environment,omitempty"`
	Severity            string `json:"severity,omitempty"`
	Hypothesis          string `json:"hypothesis,omitempty"`
	SupplementAuthority string `json:"supplement_authority"`
	ReportingRuntime    struct {
		Version       string `json:"version"`
		Commit        string `json:"commit"`
		BuildDirty    bool   `json:"build_dirty"`
		MCPAPIVersion int    `json:"mcp_api_version"`
		Platform      string `json:"platform"`
		Architecture  string `json:"architecture"`
		GoVersion     string `json:"go_version"`
	} `json:"reporting_runtime"`
	Missing    []string `json:"missing"`
	Disclaimer string   `json:"disclaimer"`
}

type Receipt struct {
	Issue      int       `json:"issue"`
	URL        string    `json:"url"`
	Outcome    string    `json:"outcome"`
	RecordedAt time.Time `json:"recorded_at"`
}

type Entry struct {
	ID        string    `json:"id"`
	Target    string    `json:"target"`
	SourceKey string    `json:"source_key"`
	Status    string    `json:"status"`
	QueuedAt  time.Time `json:"queued_at"`
	Report    Report    `json:"report"`
	Receipt   *Receipt  `json:"receipt,omitempty"`
}

type Summary struct {
	ProjectID string    `json:"project_id"`
	ReportID  string    `json:"report_id"`
	QueuedAt  time.Time `json:"queued_at"`
	ID        string    `json:"id"`
	SourceRef string    `json:"source_ref"`
	Target    string    `json:"target"`
	Status    string    `json:"status"`
	Receipt   *Receipt  `json:"receipt,omitempty"`
}

type Adapter struct {
	Config Config
	Client *http.Client
	Now    func() time.Time
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var idPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|token|password|secret)["']?\s*[=:]\s*["'])[^"'\r\n]+`),
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s"']+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|token|password|secret)\s*[=:]\s*)[^\s"']+`),
	regexp.MustCompile(`(?i)(https?://[^:/\s]+:)[^@\s]+@`),
}

func strictJSON(body []byte, value any) error {
	if err := rejectDuplicateKeys(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}

func rejectDuplicateKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var read func() error
	read = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return fmt.Errorf("duplicate or invalid JSON key")
				}
				seen[key] = true
				if err := read(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case json.Delim('['):
			for decoder.More() {
				if err := read(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
		return nil
	}
	if err := read(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}

func readBounded(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, int64(limit+1)))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, fmt.Errorf("input exceeds %d bytes", limit)
	}
	return body, nil
}

func LoadConfig(path string) (Config, error) {
	var c Config
	body, err := readBounded(path, MaxReportBytes)
	if err != nil {
		return c, err
	}
	if err := strictJSON(body, &c); err != nil {
		return c, fmt.Errorf("invalid reporting configuration")
	}
	u, err := url.Parse(c.RepositoryURL)
	if err != nil || len(c.RepositoryURL) > 2048 || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return c, fmt.Errorf("repository_url must be an HTTPS repository URL without credentials, query or fragment")
	}
	c.RepositoryURL = strings.TrimSuffix(strings.TrimSuffix(c.RepositoryURL, "/"), ".git")
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || !namePattern.MatchString(parts[0]) || !namePattern.MatchString(parts[1]) || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
		return c, fmt.Errorf("repository_url must name exactly OWNER/REPOSITORY on the forge host")
	}
	if !envPattern.MatchString(c.TokenEnv) {
		return c, fmt.Errorf("token_env must name an environment variable")
	}
	if len(c.AllowedProjectIDs) == 0 {
		return c, fmt.Errorf("allowed_project_ids must explicitly opt projects in")
	}
	for _, id := range c.AllowedProjectIDs {
		if strings.TrimSpace(id) == "" || strings.ContainsAny(id, "\x00\r\n") || len(id) > 256 {
			return c, fmt.Errorf("invalid allowed project ID")
		}
	}
	if c.Outbox == "" {
		return c, fmt.Errorf("outbox is required")
	}
	if !filepath.IsAbs(c.Outbox) {
		c.Outbox = filepath.Join(filepath.Dir(path), c.Outbox)
	}
	c.Outbox, err = filepath.Abs(c.Outbox)
	return c, err
}

func (a Adapter) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

func (a Adapter) allowed(id string) bool {
	for _, candidate := range a.Config.AllowedProjectIDs {
		if candidate == id {
			return true
		}
	}
	return false
}

func hash(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }

// sanitize traverses known wire fields before persistence. It is intentionally
// pattern-based, not a claim that arbitrary prose is free of private information.
func sanitize(body []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, err
	}
	var walk func(any) any
	walk = func(v any) any {
		switch typed := v.(type) {
		case string:
			for _, pattern := range secretPatterns {
				typed = pattern.ReplaceAllString(typed, `${1}[REDACTED]`)
			}
			return typed
		case map[string]any:
			for key, child := range typed {
				typed[key] = walk(child)
			}
		case []any:
			for index, child := range typed {
				typed[index] = walk(child)
			}
		}
		return v
	}
	return json.Marshal(walk(value))
}

func (a Adapter) Queue(path string) (*Entry, error) {
	body, err := readBounded(path, MaxReportBytes)
	if err != nil {
		return nil, err
	}
	var r Report
	if err := strictJSON(body, &r); err != nil {
		return nil, fmt.Errorf("report does not match the v1 wire interface")
	}
	body, err = sanitize(body)
	if err != nil {
		return nil, fmt.Errorf("invalid report JSON")
	}
	if err := strictJSON(body, &r); err != nil {
		return nil, fmt.Errorf("redacted report does not match the v1 wire interface")
	}
	if r.SchemaVersion != 1 || !strings.HasPrefix(r.ReportID, "sha256:") || !idPattern.MatchString(strings.TrimPrefix(r.ReportID, "sha256:")) || !strings.HasPrefix(r.Source.Ref, "work_item:") || r.Source.Ref == "work_item:" || strings.TrimSpace(r.Title) == "" || strings.TrimSpace(r.Description) == "" || r.Source.RecordedAt.IsZero() {
		return nil, fmt.Errorf("report lacks v1 identity, source, title, description or recorded time")
	}
	if !a.allowed(r.Source.ProjectID) {
		return nil, fmt.Errorf("source project is not opted in for this reporting target")
	}
	body, err = json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxReportBytes {
		return nil, fmt.Errorf("redacted report exceeds %d bytes", MaxReportBytes)
	}
	id := hash(append([]byte(a.Config.RepositoryURL+"\x00"), body...))
	entry := &Entry{ID: id, Target: a.Config.RepositoryURL, SourceKey: hash([]byte(r.Source.ProjectID + "\x00" + r.Source.Ref)), Status: "pending", QueuedAt: a.now(), Report: r}
	unlock, err := a.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	path = filepath.Join(a.Config.Outbox, id+".json")
	if _, err := os.Lstat(path); err == nil {
		return a.readEntry(path)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := a.saveEntry(entry); err != nil {
		return nil, err
	}
	return entry, nil
}

func (a Adapter) lock() (func(), error) {
	if err := os.MkdirAll(a.Config.Outbox, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(a.Config.Outbox, ".delivery.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("outbox is locked; another operation may be running (after a crash, verify it stopped before removing .delivery.lock)")
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	return func() { os.Remove(path) }, nil
}

func (a Adapter) readEntry(path string) (*Entry, error) {
	body, err := readBounded(path, MaxReportBytes+4096)
	if err != nil {
		return nil, err
	}
	var entry Entry
	if err := strictJSON(body, &entry); err != nil {
		return nil, fmt.Errorf("invalid outbox entry")
	}
	encoded, err := json.Marshal(entry.Report)
	if err != nil {
		return nil, err
	}
	if !idPattern.MatchString(entry.ID) || filepath.Base(path) != entry.ID+".json" || entry.ID != hash(append([]byte(entry.Target+"\x00"), encoded...)) || entry.SourceKey != hash([]byte(entry.Report.Source.ProjectID+"\x00"+entry.Report.Source.Ref)) {
		return nil, fmt.Errorf("outbox entry identity does not match its contents")
	}
	if entry.Status != "pending" && entry.Status != "delivered" {
		return nil, fmt.Errorf("invalid outbox status")
	}
	return &entry, nil
}

func (a Adapter) entries() ([]*Entry, error) {
	paths, err := filepath.Glob(filepath.Join(a.Config.Outbox, "*.json"))
	if err != nil {
		return nil, err
	}
	var entries []*Entry
	for _, path := range paths {
		entry, err := a.readEntry(path)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func summarize(entry *Entry) Summary {
	return Summary{ProjectID: entry.Report.Source.ProjectID, ReportID: entry.Report.ReportID, QueuedAt: entry.QueuedAt, ID: entry.ID, SourceRef: entry.Report.Source.Ref, Target: entry.Target, Status: entry.Status, Receipt: entry.Receipt}
}

func (a Adapter) Status() ([]Summary, error) {
	entries, err := a.entries()
	if err != nil {
		return nil, err
	}
	result := []Summary{}
	for _, entry := range entries {
		result = append(result, summarize(entry))
	}
	return result, nil
}

// Deliver is an explicit one-shot flush, not a daemon or lifecycle hook. A
// failed or ambiguous network result leaves the entry pending for marker-based
// recovery on the next invocation. Separate machines can still race to create.
func (a Adapter) Deliver(ctx context.Context) ([]Summary, error) {
	if !a.Config.AutomaticSubmission {
		return nil, fmt.Errorf("automatic_submission must explicitly be enabled")
	}
	token := os.Getenv(a.Config.TokenEnv)
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("configured token environment variable is empty")
	}
	unlock, err := a.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	entries, err := a.entries()
	if err != nil {
		return nil, err
	}
	var pending []*Entry
	for _, entry := range entries {
		if entry.Status == "delivered" {
			continue
		}
		if entry.Target != a.Config.RepositoryURL || !a.allowed(entry.Report.Source.ProjectID) {
			return nil, fmt.Errorf("pending report target or source is outside current opt-in; no delivery performed")
		}
		pending = append(pending, entry)
	}
	if len(pending) == 0 {
		return []Summary{}, nil
	}
	u, _ := url.Parse(a.Config.RepositoryURL)
	api := u.Scheme + "://" + u.Host + "/api/v1/repos" + u.Path
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	forge := forgeClient{client: &copyClient, api: api, token: token}
	var repo struct {
		Private   bool `json:"private"`
		HasIssues bool `json:"has_issues"`
		Archived  bool `json:"archived"`
	}
	if _, err := forge.request(ctx, http.MethodGet, "", nil, &repo); err != nil {
		return nil, err
	}
	if !repo.Private || !repo.HasIssues || repo.Archived {
		return nil, fmt.Errorf("target must be private, unarchived and have issues enabled; no report submitted")
	}
	result := []Summary{}
	for _, entry := range pending {
		receipt, err := forge.deliver(ctx, entry)
		if err != nil {
			return result, err
		}
		receipt.RecordedAt, receipt.URL = a.now(), a.Config.RepositoryURL+"/issues/"+strconv.Itoa(receipt.Issue)
		entry.Status, entry.Receipt = "delivered", receipt
		if err := a.saveEntry(entry); err != nil {
			return result, fmt.Errorf("remote delivery succeeded but receipt could not be saved; next delivery must recover from the marker")
		}
		result = append(result, summarize(entry))
	}
	return result, nil
}

func (a Adapter) saveEntry(entry *Entry) error {
	body, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if len(body)+1 > MaxReportBytes+4096 {
		return fmt.Errorf("outbox entry exceeds its storage budget")
	}
	file, err := os.CreateTemp(a.Config.Outbox, ".entry-")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	_, err = file.Write(append(body, '\n'))
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(a.Config.Outbox, entry.ID+".json"))
}

type forgeClient struct {
	client     *http.Client
	api, token string
}

func (f forgeClient) request(ctx context.Context, method, path string, payload any, result any) (http.Header, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, f.api+path, body)
	if err != nil {
		return nil, fmt.Errorf("cannot construct Forgejo request")
	}
	request.Header.Set("Authorization", "token "+f.token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := f.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Forgejo request failed; pending report retained")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Forgejo returned HTTP %d; pending report retained", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return nil, fmt.Errorf("Forgejo response unavailable or too large; pending report retained")
	}
	if err := json.Unmarshal(data, result); err != nil {
		return nil, fmt.Errorf("invalid Forgejo response; pending report retained")
	}
	return response.Header, nil
}

type issue struct {
	Index       int             `json:"number"`
	Body        string          `json:"body"`
	PullRequest json.RawMessage `json:"pull_request"`
}

func nextPage(header http.Header, count, seen int) bool {
	if strings.Contains(header.Get("Link"), `rel="next"`) {
		return true
	}
	if total, err := strconv.Atoi(header.Get("X-Total-Count")); err == nil {
		return seen < total
	}
	return count >= 50
}

func (f forgeClient) deliver(ctx context.Context, entry *Entry) (*Receipt, error) {
	sourceMarker := "<!-- llewm-source:" + entry.SourceKey + " -->"
	reportMarker := "<!-- llewm-report:" + entry.ID + " -->"
	var matching *issue
	seen := 0
	for page := 1; page <= 100; page++ {
		var issues []issue
		header, err := f.request(ctx, http.MethodGet, fmt.Sprintf("/issues?state=all&type=issues&limit=50&page=%d", page), nil, &issues)
		if err != nil {
			return nil, err
		}
		for _, candidate := range issues {
			if len(candidate.PullRequest) > 0 && string(candidate.PullRequest) != "null" {
				continue
			}
			if strings.Contains(candidate.Body, "<!-- llmem-source:"+entry.SourceKey+" -->") {
				return nil, fmt.Errorf("matching issue uses pre-release LLMem markers; review and explicitly convert its source/report markers before delivery; no report submitted")
			}
			if strings.Contains(candidate.Body, sourceMarker) {
				if matching != nil {
					return nil, fmt.Errorf("several issues match the source; maintainer review required")
				}
				copy := candidate
				matching = &copy
			}
		}
		seen += len(issues)
		if !nextPage(header, len(issues), seen) {
			break
		}
		if page == 100 {
			return nil, fmt.Errorf("issue scan budget exhausted; no report submitted")
		}
	}
	if matching != nil {
		if matching.Index <= 0 {
			return nil, fmt.Errorf("invalid matching issue identity")
		}
		if strings.Contains(matching.Body, "<!-- llmem-report:"+entry.ID+" -->") {
			return nil, fmt.Errorf("matching issue uses pre-release LLMem report marker; explicitly convert its markers before delivery; no report submitted")
		}
		if strings.Contains(matching.Body, reportMarker) {
			return &Receipt{Issue: matching.Index, Outcome: "recovered"}, nil
		}
		seen = 0
		for page := 1; page <= 100; page++ {
			var comments []struct {
				Body string `json:"body"`
			}
			header, err := f.request(ctx, http.MethodGet, fmt.Sprintf("/issues/%d/comments?limit=50&page=%d", matching.Index, page), nil, &comments)
			if err != nil {
				return nil, err
			}
			for _, comment := range comments {
				if strings.Contains(comment.Body, "<!-- llmem-report:"+entry.ID+" -->") {
					return nil, fmt.Errorf("matching report comment uses pre-release LLMem markers; review and explicitly convert its markers before delivery; no report submitted")
				}
				if strings.Contains(comment.Body, reportMarker) {
					return &Receipt{Issue: matching.Index, Outcome: "recovered"}, nil
				}
			}
			seen += len(comments)
			if !nextPage(header, len(comments), seen) {
				break
			}
			if page == 100 {
				return nil, fmt.Errorf("comment scan budget exhausted; no report submitted")
			}
		}
		var response struct {
			ID int64 `json:"id"`
		}
		if _, err := f.request(ctx, http.MethodPost, fmt.Sprintf("/issues/%d/comments", matching.Index), map[string]string{"body": reportBody(entry, sourceMarker, reportMarker)}, &response); err != nil {
			return nil, err
		}
		if response.ID <= 0 {
			return nil, fmt.Errorf("Forgejo omitted comment identity; report retained for recovery")
		}
		return &Receipt{Issue: matching.Index, Outcome: "attached"}, nil
	}
	var created issue
	if _, err := f.request(ctx, http.MethodPost, "/issues", map[string]string{"title": entry.Report.Title, "body": reportBody(entry, sourceMarker, reportMarker)}, &created); err != nil {
		return nil, err
	}
	if created.Index <= 0 {
		return nil, fmt.Errorf("Forgejo omitted issue identity; report retained for recovery")
	}
	return &Receipt{Issue: created.Index, Outcome: "created"}, nil
}

func reportBody(entry *Entry, sourceMarker, reportMarker string) string {
	body, _ := json.MarshalIndent(entry.Report, "", "  ")
	// Four backticks avoid accidentally closing the fence with a normal triple
	// backtick in prose; JSON strings escape line breaks, so no injected fence
	// can appear at the start of a separate line.
	return sourceMarker + "\n" + reportMarker + "\n\nSelected Llewm bug report. Source authority and hypothesis are distinct; diagnostics describe the reporter.\n\n````json\n" + string(body) + "\n````\n"
}

// StatusSnapshot is the v1 read-only console/IDE wire interface. It contains
// metadata and receipts only; credentials and selected report bodies are absent.
type StatusSnapshot struct {
	SchemaVersion int       `json:"schema_version"`
	ObservedAt    time.Time `json:"observed_at"`
	Authority     string    `json:"authority"`
	Reports       []Summary `json:"reports"`
}

func (a Adapter) Snapshot(projectID string) (*StatusSnapshot, error) {
	reports, err := a.Status()
	if err != nil {
		return nil, err
	}
	if projectID != "" {
		scoped := []Summary{}
		for _, report := range reports {
			if report.ProjectID == projectID {
				scoped = append(scoped, report)
			}
		}
		reports = scoped
	}
	if len(reports) > 1000 {
		return nil, fmt.Errorf("status snapshot exceeds 1000 reports")
	}
	snapshot := &StatusSnapshot{SchemaVersion: 1, ObservedAt: a.now(), Authority: "machine_observed", Reports: reports}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if len(body)+1 > 1<<20 {
		return nil, fmt.Errorf("status snapshot exceeds 1 MiB")
	}
	return snapshot, nil
}
