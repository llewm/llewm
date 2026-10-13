// Package clientperm derives the client permission rules Continuity needs so
// that a client does not prompt for, or its auto-mode classifier does not
// block, the user's own Continuity bookkeeping and the procedures a standing
// grant allows. An agent cannot grant itself permissions, so a person applies
// these rules: they are printed, or merged into a settings file by a command
// the person runs at a terminal.
package clientperm

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/standinggrant"
)

// Clients names the supported clients.
const (
	ClaudeCode = "claude-code"
	Codex      = "codex"
)

// Claude Code rule values. The MCP rule allows every tool of the plugin's
// continuity server.
const (
	ClaudeMCPRule = "mcp__plugin_llewm_continuity"
	// CodexPlugin is the plugin key under which Codex stores per-tool approvals.
	CodexPlugin = "llewm@llewm"
)

// Executables are the names the llewm CLI is installed under.
var Executables = []string{"llewm"}

// AutoModeNote explains the auto-mode guidance, which this package never
// writes: the setting belongs to the person and its schema is the client's.
const AutoModeNote = "Claude Code's auto mode can still classify Continuity writes (checkpoint text quotes instructions) even with these rules. If it blocks them, describe the Continuity store as your own routine bookkeeping in your auto-mode settings; see docs/installation.md#client-permissions."

// GrantRules are the rules one active standing grant contributes.
type GrantRules struct {
	GrantID string   `json:"grant_id"`
	Grant   string   `json:"grant"`
	Rules   []string `json:"rules"`
}

// Rules is what a client needs.
type Rules struct {
	Client string       `json:"client"`
	Allow  []string     `json:"allow"`
	Deny   []string     `json:"deny"`
	Grants []GrantRules `json:"grants"`
	// Codex only: per-tool approvals (tool names) and exec-policy rules.
	CodexTools []string `json:"codex_tools,omitempty"`
	Note       string   `json:"note,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// Build derives the rules for a client from the Continuity tools and the
// project's active standing grants. toolNames is only needed for Codex.
func Build(ctx context.Context, db *sql.DB, projectID, client string, toolNames []string) (Rules, error) {
	grants, err := (standinggrant.Service{DB: db}).List(ctx, projectID, false)
	if err != nil {
		return Rules{}, err
	}
	result := Rules{Client: client, Allow: []string{}, Deny: []string{}, Grants: []GrantRules{}}
	store := procedures.Service{DB: db}
	var commands []GrantRules
	for _, grant := range grants {
		entry := GrantRules{GrantID: grant.ID, Grant: grant.Ref(), Rules: []string{}}
		for _, covered := range grant.Covers {
			procedure, err := store.Get(ctx, projectID, covered.ProcedureID, covered.Revision)
			if err != nil {
				return Rules{}, err
			}
			if procedure == nil {
				continue
			}
			values := parameterValues(*procedure, grant)
			for _, step := range procedure.Steps {
				if rendered := Render(step.ArgvTemplate, values); len(step.ArgvTemplate) > 0 {
					if value := rule(client, rendered); value != "" {
						entry.Rules = appendUnique(entry.Rules, value)
					} else {
						result.Warnings = append(result.Warnings, fmt.Sprintf("Omitted unresolved command in %s step %s: bind every parameter before generating a %s rule.", covered.ProcedureID, step.ID, client))
					}
				}
			}
		}
		commands = append(commands, entry)
	}
	switch client {
	case ClaudeCode:
		result.Allow = append(result.Allow, ClaudeMCPRule)
		for _, name := range Executables {
			result.Allow = append(result.Allow, "Bash("+name+" *)")
			result.Deny = append(result.Deny, "Bash("+name+" standing-grant create *)")
		}
		result.Note = AutoModeNote
	case Codex:
		for _, name := range Executables {
			result.Allow = append(result.Allow, fmt.Sprintf(`prefix_rule(pattern=[%q], decision="allow")`, name))
			result.Deny = append(result.Deny, fmt.Sprintf(`prefix_rule(pattern=[%q, "standing-grant", "create"], decision="forbidden")`, name))
		}
		result.CodexTools = append([]string{}, toolNames...)
		result.Note = "Scope: the CLI rules and MCP approvals allow reads AND writes, plus bound standing-grant command prefixes. They do not grant filesystem or network access. Run --write yourself to preview and apply managed entries under CODEX_HOME (default ~/.codex). Existing tool settings and prompt/deny rules remain authoritative; doctor checks presence, not effective policy."
	default:
		return Rules{}, fmt.Errorf("client must be %s or %s, not %q", ClaudeCode, Codex, client)
	}
	for _, entry := range commands {
		result.Grants = append(result.Grants, entry)
		for _, value := range entry.Rules {
			result.Allow = appendUnique(result.Allow, value)
		}
	}
	return result, nil
}

var placeholder = regexp.MustCompile(`\{\{([A-Za-z0-9_.-]+)\}\}`)

// Rendered is an argv template with its parameter values filled in, up to the
// first parameter that has no value.
type Rendered struct {
	// Args are the fully resolved arguments before that parameter.
	Args []string
	// Partial is the resolved start of the argument holding it, if any.
	Partial string
	// Exact is true when every argument was resolved.
	Exact bool
}

// Render fills an argv template with values.
func Render(argv []string, values map[string]string) Rendered {
	var result Rendered
	for _, arg := range argv {
		resolved, last := "", 0
		for _, match := range placeholder.FindAllStringSubmatchIndex(arg, -1) {
			value, ok := values[arg[match[2]:match[3]]]
			if !ok {
				result.Partial = resolved + arg[last:match[0]]
				return result
			}
			resolved += arg[last:match[0]] + value
			last = match[1]
		}
		result.Args = append(result.Args, resolved+arg[last:])
	}
	result.Exact = true
	return result
}

// Command renders a shell command, quoting arguments as a person or agent
// would type them. An unresolved command ends where the missing value began;
// inside a quoted argument the quote stays open.
func (r Rendered) Command() string {
	parts := make([]string, 0, len(r.Args)+1)
	for _, arg := range r.Args {
		parts = append(parts, quote(arg))
	}
	if r.Partial != "" {
		quoted := quote(r.Partial + "x")
		parts = append(parts, strings.TrimSuffix(strings.TrimSuffix(quoted, "'"), "x"))
	}
	return strings.Join(parts, " ")
}

func quote(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n'\"\\$`&|;<>()*?[]{}!#~") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func rule(client string, rendered Rendered) string {
	if len(rendered.Args) == 0 {
		return ""
	}
	if client == Codex {
		if !rendered.Exact {
			return ""
		}
		quoted := make([]string, len(rendered.Args))
		for i, arg := range rendered.Args {
			quoted[i] = fmt.Sprintf("%q", arg)
		}
		return fmt.Sprintf(`prefix_rule(pattern=[%s], decision="allow")`, strings.Join(quoted, ", "))
	}
	switch {
	case rendered.Exact:
		return "Bash(" + rendered.Command() + ")"
	case rendered.Partial != "":
		return "Bash(" + rendered.Command() + "*)"
	}
	return "Bash(" + rendered.Command() + " *)"
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// ClaudeSnippet renders the settings fragment for Claude Code.
func ClaudeSnippet(rules Rules) ([]byte, error) {
	data, err := marshal(map[string]any{"permissions": map[string]any{"allow": rules.Allow, "deny": rules.Deny}})
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, data, "", "  "); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// SettingsPath resolves a Claude Code settings scope: user (every project),
// local (this project, personal), or project (this project, shared).
func SettingsPath(scope, home, root string) (string, error) {
	switch scope {
	case "user":
		return filepath.Join(home, ".claude", "settings.json"), nil
	case "local":
		return filepath.Join(root, ".claude", "settings.local.json"), nil
	case "project":
		return filepath.Join(root, ".claude", "settings.json"), nil
	}
	return "", fmt.Errorf("scope must be user, local, or project, not %q", scope)
}

// marshal encodes JSON without escaping &, <, and >, so rules such as
// `a && b` stay readable in settings files.
func marshal(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

type member struct {
	key   string
	value json.RawMessage
}

func decodeObject(data []byte) ([]member, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("settings must be a JSON object")
	}
	var members []member
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		members = append(members, member{key, value})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err == nil {
		return nil, errors.New("settings has data after the JSON object")
	}
	return members, nil
}

func encodeObject(members []member) ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			out.WriteByte(',')
		}
		key, err := marshal(m.key)
		if err != nil {
			return nil, err
		}
		out.Write(key)
		out.WriteByte(':')
		out.Write(m.value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func find(members []member, key string) int {
	for i, m := range members {
		if m.key == key {
			return i
		}
	}
	return -1
}

// MergeClaude adds the rules to a Claude Code settings document, keeping its
// other keys, their order, and existing rules. It returns the new document
// (indented) and the rules it added; with nothing to add, the input is
// returned unchanged. Empty input is a new document.
func MergeClaude(data []byte, allow, deny []string) ([]byte, []string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	top, err := decodeObject(data)
	if err != nil {
		return nil, nil, err
	}
	var permissions []member
	index := find(top, "permissions")
	if index >= 0 {
		if permissions, err = decodeObject(top[index].value); err != nil {
			return nil, nil, fmt.Errorf("permissions: %w", err)
		}
	}
	var added []string
	for _, list := range []struct {
		key    string
		values []string
	}{{"allow", allow}, {"deny", deny}} {
		var existing []string
		at := find(permissions, list.key)
		if at >= 0 {
			if err := json.Unmarshal(permissions[at].value, &existing); err != nil {
				return nil, nil, fmt.Errorf("permissions.%s must be a list of strings: %w", list.key, err)
			}
		}
		changed := false
		for _, value := range list.values {
			if !contains(existing, value) {
				existing = append(existing, value)
				added = append(added, list.key+": "+value)
				changed = true
			}
		}
		if !changed {
			continue
		}
		encoded, err := marshal(existing)
		if err != nil {
			return nil, nil, err
		}
		if at >= 0 {
			permissions[at].value = encoded
		} else {
			permissions = append(permissions, member{list.key, encoded})
		}
	}
	if len(added) == 0 {
		return data, nil, nil
	}
	encoded, err := encodeObject(permissions)
	if err != nil {
		return nil, nil, err
	}
	if index >= 0 {
		top[index].value = encoded
	} else {
		top = append(top, member{"permissions", encoded})
	}
	raw, err := encodeObject(top)
	if err != nil {
		return nil, nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return nil, nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), added, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// Status is one detected client's permission setup.
type Status struct {
	Client string `json:"client"`
	// Configured lists the files that hold the rules.
	Configured []string `json:"configured"`
	// Missing lists what is absent; empty means the client is set up.
	Missing []string `json:"missing"`
}

// Name is the client's display name.
func (s Status) Name() string { return clientName(s.Client) }

// OK reports whether nothing is missing.
func (s Status) OK() bool { return len(s.Missing) == 0 }

// Warning is the doctor and readiness text, or "".
func (s Status) Warning() string {
	if s.OK() {
		return ""
	}
	return fmt.Sprintf("%s lacks Continuity permission rules (%s), so it may prompt for or block Continuity work; run `llewm client permissions --client %s` in a terminal to see them%s", clientName(s.Client), strings.Join(s.Missing, "; "), s.Client, fixHint(s.Client))
}

func clientName(client string) string {
	if client == Codex {
		return "Codex"
	}
	return "Claude Code"
}

func fixHint(client string) string {
	if client == ClaudeCode {
		return " and add them with --write --scope user"
	}
	return " and review/apply them with --write"
}

// Homes locates client configuration: the user's home and, for Codex,
// $CODEX_HOME when set.
type Homes struct {
	Home      string
	CodexHome string
}

// DefaultHomes reads the user's home and CODEX_HOME.
func DefaultHomes() (Homes, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Homes{}, err
	}
	codex := os.Getenv("CODEX_HOME")
	if codex == "" {
		codex = filepath.Join(home, ".codex")
	}
	return Homes{Home: home, CodexHome: codex}, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// Detect checks every supported client whose configuration directory exists
// (~/.claude, and $CODEX_HOME or ~/.codex); clients not installed are left
// out, so nobody is warned about a client they do not use. toolNames, when
// given, lets the Codex check count unapproved Continuity tools.
func Detect(homes Homes, root string, toolNames []string) []Status {
	var result []Status
	if isDir(filepath.Join(homes.Home, ".claude")) {
		result = append(result, checkClaude(homes.Home, root))
	}
	if homes.CodexHome != "" && isDir(homes.CodexHome) {
		result = append(result, checkCodex(homes.CodexHome, toolNames))
	}
	return result
}

// Warnings returns the warnings of the statuses that are not OK.
func Warnings(statuses []Status) []string {
	result := []string{}
	for _, status := range statuses {
		if warning := status.Warning(); warning != "" {
			result = append(result, warning)
		}
	}
	return result
}

// checkClaude looks for the base rules (the MCP tools and the CLI) in the
// user, project-local, and project settings files; a file counts when both
// are in its permissions.allow. Unreadable or absent files are skipped.
func checkClaude(home, root string) Status {
	status := Status{Client: ClaudeCode, Configured: []string{}, Missing: []string{}}
	for _, scope := range []string{"user", "local", "project"} {
		path, _ := SettingsPath(scope, home, root)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var settings struct {
			Permissions struct {
				Allow []string `json:"allow"`
			} `json:"permissions"`
		}
		if json.Unmarshal(data, &settings) != nil {
			continue
		}
		mcp, cli := false, false
		for _, value := range settings.Permissions.Allow {
			if value == ClaudeMCPRule || strings.HasPrefix(value, ClaudeMCPRule+"__") {
				mcp = true
			}
			for _, name := range Executables {
				if value == "Bash("+name+" *)" || value == "Bash("+name+":*)" || value == "Bash("+name+")" {
					cli = true
				}
			}
		}
		if mcp && cli {
			status.Configured = append(status.Configured, path)
		}
	}
	if len(status.Configured) == 0 {
		status.Missing = append(status.Missing, "no settings file allows both the Continuity MCP tools and the llewm CLI")
	}
	sort.Strings(status.Configured)
	return status
}

var codexAllow = regexp.MustCompile(`prefix_rule\(\s*pattern\s*=\s*\[\s*"llewm"\s*\]\s*,\s*decision\s*=\s*"allow"\s*\)`)

// checkCodex looks for an exec-policy rule allowing the llewm CLI in
// CODEX_HOME/rules/*.rules and for approved Continuity tools in
// CODEX_HOME/config.toml. The format is inferred from a working Codex
// configuration, not from Codex documentation.
func checkCodex(codexHome string, toolNames []string) Status {
	status := Status{Client: Codex, Configured: []string{}, Missing: []string{}}
	rules, _ := filepath.Glob(filepath.Join(codexHome, "rules", "*.rules"))
	sort.Strings(rules)
	cli := false
	for _, path := range rules {
		if data, err := os.ReadFile(path); err == nil && codexAllow.Match(data) {
			status.Configured = append(status.Configured, path)
			cli = true
			break
		}
	}
	if !cli {
		status.Missing = append(status.Missing, "no exec-policy rule allows the llewm CLI")
	}
	config := filepath.Join(codexHome, "config.toml")
	approved := codexApprovedTools(config)
	switch {
	case len(approved) == 0:
		status.Missing = append(status.Missing, "no Continuity MCP tool is approved")
	case toolNames != nil:
		missing := 0
		for _, name := range toolNames {
			if !approved[name] {
				missing++
			}
		}
		if missing > 0 {
			status.Missing = append(status.Missing, fmt.Sprintf("%d of %d Continuity MCP tools are not approved", missing, len(toolNames)))
		}
	}
	if len(approved) > 0 {
		status.Configured = append(status.Configured, config)
	}
	return status
}

func codexApprovedTools(path string) map[string]bool {
	var config struct {
		Plugins map[string]struct {
			MCPServers map[string]struct {
				Tools map[string]struct {
					ApprovalMode string `toml:"approval_mode"`
				} `toml:"tools"`
			} `toml:"mcp_servers"`
		} `toml:"plugins"`
	}
	approved := map[string]bool{}
	if _, err := toml.DecodeFile(path, &config); err != nil {
		return approved
	}
	for name, tool := range config.Plugins[CodexPlugin].MCPServers["continuity"].Tools {
		if tool.ApprovalMode == "approve" {
			approved[name] = true
		}
	}
	return approved
}
