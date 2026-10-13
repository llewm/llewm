package codestats

import (
	"reflect"
	"testing"
)

const fixtureLog = "\x00aaaaaaaaaaaa1111\t2026-10-01T10:00:00+00:00\tAda\tAdd parser\n\n" +
	"10\t2\tcmd/main.go\n5\t0\tdocs/guide.md\n-\t-\tassets/logo.png\n3\t3\tgo.sum\n" +
	"\x00bbbbbbbbbbbb2222\t2026-10-02T09:30:00+00:00\tBo\tFix docs\n\n" +
	"0\t4\tdocs/guide.md\n7\t1\tvendor/lib/x.go\n"

func TestParseAndBuild(t *testing.T) {
	changes, err := Parse([]byte(fixtureLog), "member/")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 6 || changes[0].Path != "member/cmd/main.go" || changes[0].Added != 10 || !changes[2].Binary || changes[4].Author != "Bo" {
		t.Fatalf("parsed %+v", changes)
	}
	report, err := Build(changes, Options{By: "language"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{{Key: "Go", Commits: 1, Files: 1, Added: 10, Removed: 2}, {Key: "Markdown", Commits: 2, Files: 1, Added: 5, Removed: 4}, {Key: ".png", Commits: 1, Files: 1}}
	if !reflect.DeepEqual(report.Rows, want) {
		t.Fatalf("rows %+v", report.Rows)
	}
	if report.Total != (Row{Commits: 2, Files: 3, Added: 15, Removed: 6}) || report.Excluded != (Row{Commits: 2, Files: 2, Added: 10, Removed: 4}) || report.Unattributed != nil {
		t.Fatalf("total %+v excluded %+v", report.Total, report.Excluded)
	}
	byDay, _ := Build(changes, Options{By: "day"}, nil)
	if byDay.Rows[0].Key != "2026-10-02" || byDay.Rows[1].Key != "2026-10-01" {
		t.Fatalf("days %+v", byDay.Rows)
	}
	byCommit, _ := Build(changes, Options{By: "commit", Limit: 1}, nil)
	if len(byCommit.Rows) != 1 || byCommit.Rows[0].Key != "aaaaaaaaaaaa Add parser" || !byCommit.Truncated {
		t.Fatalf("commits %+v", byCommit)
	}
	if withoutDocs, _ := Build(changes, Options{By: "commit", Exclude: []string{"*.md"}}, nil); len(withoutDocs.Rows) != 1 || withoutDocs.Excluded.Removed != 8 {
		t.Fatalf("config exclude %+v", withoutDocs)
	}
	attributed, _ := Build(changes, Options{By: "task"}, map[string][]string{"aaaaaaaaaaaa1111": {"t1", "t2"}})
	if len(attributed.Rows) != 2 || attributed.Rows[0].Added != 15 || attributed.Unattributed.Commits != 1 || attributed.Unattributed.Removed != 4 {
		t.Fatalf("attributed %+v unattributed %+v", attributed.Rows, attributed.Unattributed)
	}
	if _, err := Build(changes, Options{By: "color"}, nil); err == nil {
		t.Fatal("unknown grouping accepted")
	}
}

func TestExcludedAndLanguage(t *testing.T) {
	for path, want := range map[string]bool{"vendor/a.go": true, "x/node_modules/y.js": true, "go.sum": true, "sub/go.sum": true, "a/b.lock": true, "vendored/a.go": false, "main.go": false} {
		if got := Excluded(path, DefaultExclude); got != want {
			t.Errorf("Excluded(%q) = %v", path, got)
		}
	}
	for path, want := range map[string]string{"a/b.go": "Go", "Makefile": "Make", "x.YML": "YAML", "LICENSE": "other", "a.zig": ".zig"} {
		if got := Language(path); got != want {
			t.Errorf("Language(%q) = %q", path, got)
		}
	}
}

func TestPrefixResolver(t *testing.T) {
	resolve := prefixResolver([]string{"abcdef0123", "abcdef0999", "1234567aaa"})
	if got := resolve("abcdef01"); !reflect.DeepEqual(got, []string{"abcdef0123"}) {
		t.Fatalf("got %v", got)
	}
	if got := resolve("abcdef0"); len(got) != 2 {
		t.Fatalf("ambiguous prefix got %v", got)
	}
	if got := resolve("abc"); got != nil {
		t.Fatalf("short prefix got %v", got)
	}
}
