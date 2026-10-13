package pmadapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestPublishedConformance(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "pm-adapter")
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "pm-adapter-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(root, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		File  string
		Valid bool
		Codes []string
		Error string
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.File, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, c.File))
			if err != nil {
				t.Fatal(err)
			}
			r, err := Inspect(data, Project{"example", "tenant", "project"})
			if !c.Valid {
				if err == nil || !strings.Contains(err.Error(), c.Error) {
					t.Fatalf("got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			if err := resolved.Validate(value); err != nil {
				t.Fatalf("published schema disagrees: %v", err)
			}
			codes := []string{}
			for _, d := range r.Diagnostics {
				codes = append(codes, d.Code)
			}
			if !reflect.DeepEqual(codes, c.Codes) {
				t.Fatalf("codes %v, want %v", codes, c.Codes)
			}
			if strings.Contains(string(mustJSON(r)), "DO_NOT_PRINT") {
				t.Fatal("credential escaped redaction")
			}
		})
	}
}
