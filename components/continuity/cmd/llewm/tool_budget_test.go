package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolDefinitionBudget bounds what an eager MCP client puts in front of the
// model on every request: each tool's name, description, and input schema.
// The interface cost audit measured 47 KB (about 12.8k tokens); see
// docs/interface-cost-audit.md. Raise it only with a reason.
const toolDefinitionBudget = 20 << 10

func TestMCPToolDefinitionsFitTheBudget(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPProcess$")
	child.Dir = t.TempDir()
	child.Env = append(os.Environ(), "LLEWM_TEST_MCP_PROCESS=1", "LLEWM_TEST_PROJECT_ROOT="+root)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "budget", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: child}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		name  string
		bytes int
	}
	var rows []row
	total := 0
	for _, tool := range list.Tools {
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		size := len(tool.Name) + len(tool.Description) + len(schema)
		rows = append(rows, row{tool.Name, size})
		total += size
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].bytes > rows[j].bytes })
	for _, value := range rows {
		t.Logf("%6d %s", value.bytes, value.name)
	}
	t.Logf("%6d total for %d tools (budget %d)", total, len(rows), toolDefinitionBudget)
	if total > toolDefinitionBudget {
		t.Fatalf("tool definitions are %d bytes, over the %d-byte budget", total, toolDefinitionBudget)
	}
}
