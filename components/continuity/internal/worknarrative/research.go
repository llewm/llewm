package worknarrative

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const MaxCitations = 16

var privateURLKey = regexp.MustCompile(`(?i)(token|password|secret|signature|credential|api.?key|authorization|^x-amz-|^x-goog-)`)

// NormalizeResearch is shared by live writes and portable validation. A source
// URL is preserved exactly after whitespace trimming, never silently rewritten.
func NormalizeResearch(source *Source, claims []Claim) (*Source, []Claim, error) {
	if source != nil {
		v := *source
		v.URL = strings.TrimSpace(v.URL)
		u, err := url.Parse(v.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || len(v.URL) > 4096 || redact.String(v.URL) != v.URL || strings.ContainsAny(v.URL, "\r\n\x00") {
			return nil, nil, fmt.Errorf("source URL must be an HTTP(S) reference without credentials")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return nil, nil, fmt.Errorf("source URL has invalid query parameters")
		}
		for k := range query {
			if privateURLKey.MatchString(k) {
				return nil, nil, fmt.Errorf("source URL contains a credential or signed-query parameter")
			}
		}
		observed, err := time.Parse(time.RFC3339Nano, v.AccessedAt)
		if err != nil {
			return nil, nil, fmt.Errorf("source accessed_at must be RFC3339")
		}
		v.AccessedAt = observed.UTC().Format(time.RFC3339Nano)
		if !authorities[v.Authority] {
			return nil, nil, fmt.Errorf("source authority is required")
		}
		for _, field := range []*string{&v.Publisher, &v.PublishedAt, &v.Version} {
			*field = strings.TrimSpace(redact.String(*field))
			if len(*field) > 512 || strings.ContainsAny(*field, "\x00\r\n") {
				return nil, nil, fmt.Errorf("source metadata must be one line of at most 512 bytes")
			}
		}
		if v.PublishedAt != "" {
			if _, err := time.Parse("2006-01-02", v.PublishedAt); err != nil {
				if _, err := time.Parse(time.RFC3339Nano, v.PublishedAt); err != nil {
					return nil, nil, fmt.Errorf("source published_at must be an ISO date or RFC3339")
				}
			}
		}
		if v.ContentSHA256 != "" && !hashPattern.MatchString(v.ContentSHA256) {
			return nil, nil, fmt.Errorf("source content SHA-256 is invalid")
		}
		source = &v
	}
	result := append([]Claim(nil), claims...)
	for i := range result {
		if len(result[i].Citations) > MaxCitations {
			return nil, nil, fmt.Errorf("claim accepts at most %d citations", MaxCitations)
		}
		result[i].Citations = append([]Citation(nil), result[i].Citations...)
		seen := map[Citation]bool{}
		for j := range result[i].Citations {
			c := &result[i].Citations[j]
			c.Source = strings.TrimSpace(c.Source)
			id := strings.TrimPrefix(c.Source, "narrative:")
			if id == c.Source || id == "" || len(id) > 256 || strings.ContainsAny(id, "#\r\n\x00") || redact.String(id) != id {
				return nil, nil, fmt.Errorf("citation source must be an exact narrative:ID observation")
			}
			if c.Relation != "supports" && c.Relation != "contradicts" && c.Relation != "context" {
				return nil, nil, fmt.Errorf("citation relation must be supports, contradicts, or context")
			}
			c.Locator = strings.TrimSpace(redact.String(c.Locator))
			if len(c.Locator) > 1024 || strings.ContainsAny(c.Locator, "\x00\r\n") {
				return nil, nil, fmt.Errorf("citation locator must be one line of at most 1024 bytes")
			}
			if seen[*c] {
				return nil, nil, fmt.Errorf("duplicate citation in claim")
			}
			seen[*c] = true
		}
	}
	return source, result, nil
}

type citationReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func ValidateCitationTargets(ctx context.Context, db citationReader, projectID string, claims []Claim) error {
	for _, claim := range claims {
		for _, citation := range claim.Citations {
			var count int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_narratives WHERE project_id=? AND id=? AND json_type(payload_json,'$.source')='object'`, projectID, strings.TrimPrefix(citation.Source, "narrative:")).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("citation target %s is not a source observation in this project", citation.Source)
			}
		}
	}
	return nil
}

func RenderSource(v *Source) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("Source: %s\nAccessed: %s (%s)\nPublisher: %s\nPublished: %s\nVersion: %s\nContent SHA-256: %s\n", v.URL, v.AccessedAt, v.Authority, known(v.Publisher), known(v.PublishedAt), known(v.Version), known(v.ContentSHA256))
}

func known(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
