package contracts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEndsAtTheNextHeadingOfTheSameLevel(t *testing.T) {
	text := "# Doc\n\n<!-- continuity:contract a -->\n## A\n\nkind: axiom\nscope: everything\n\nFirst line.\n\n### Detail\n\nStill A.\n\n## B\n\nNot A.\n<!-- continuity:contract b -->\n## Broken\n\nNo kind here.\n"
	contracts, problems := Parse("doc.md", text)
	if len(contracts) != 1 || contracts[0].ID != "a" || contracts[0].Kind != "axiom" || contracts[0].Scope != "everything" || contracts[0].Line != 3 {
		t.Fatalf("contracts: %+v", contracts)
	}
	if !strings.HasPrefix(contracts[0].Statement, "First line.") || !strings.Contains(contracts[0].Statement, "Still A.") || strings.Contains(contracts[0].Statement, "Not A.") {
		t.Fatalf("statement: %q", contracts[0].Statement)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], `contract "b" needs a kind`) {
		t.Fatalf("problems: %v", problems)
	}
	again, _ := Parse("doc.md", strings.Replace(text, "Still A.", "Changed.", 1))
	if again[0].SHA256 == contracts[0].SHA256 {
		t.Fatal("a changed statement kept its hash")
	}
}

func TestParseIgnoresFencedExamples(t *testing.T) {
	text := "<!-- continuity:contract real -->\n## Real\n\nkind: promise\n\nSee:\n\n```markdown\n<!-- continuity:contract example -->\n## Example\n```\n\nDone.\n"
	contracts, problems := Parse("doc.md", text)
	if len(problems) != 0 || len(contracts) != 1 || contracts[0].ID != "real" || !strings.Contains(contracts[0].Statement, "## Example") || !strings.HasSuffix(contracts[0].Statement, "Done.") {
		t.Fatalf("%+v %v", contracts, problems)
	}
	if contracts, _ := Parse("doc.md", "```\n<!-- continuity:contract example -->\n## Example\nkind: axiom\n\nText\n```\n"); len(contracts) != 0 {
		t.Fatalf("fenced example parsed: %+v", contracts)
	}
}

func TestRepositoryContractsParse(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	for path, id := range map[string]string{
		"AGENTS.md": "pre-release-breaking-changes",
		"components/continuity/docs/memory-repository.md":       "memory-repository",
		"components/continuity/docs/client-hook-conformance.md": "client-hooks",
		"components/continuity/docs/contracts.md":               "",
	} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		contracts, problems := Parse(path, string(data))
		if id == "" {
			if len(problems) != 0 || len(contracts) != 0 {
				t.Fatalf("%s: examples parsed: %+v %v", path, contracts, problems)
			}
			continue
		}
		if len(problems) != 0 || len(contracts) != 1 || contracts[0].ID != id {
			t.Fatalf("%s: %+v %v", path, contracts, problems)
		}
	}
}
