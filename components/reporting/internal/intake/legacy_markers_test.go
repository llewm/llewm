package intake

import (
	"net/http"
	"strings"
	"testing"
)

func TestLegacyMarkersRefuseDeliveryBeforeDuplicateSubmission(t *testing.T) {
	for _, location := range []string{"source", "report", "comment"} {
		t.Run(location, func(t *testing.T) {
			t.Setenv("LLEWM_TEST_TOKEN", "fixture-credential-secret")
			dir := t.TempDir()
			state := &forgeState{private: true, comments: map[int][]string{}}
			server := newFixtureServer(http.HandlerFunc(state.handler))
			defer server.Close()
			adapter := Adapter{Config: writeConfig(t, server.URL, dir), Client: server.Client()}
			entry, err := adapter.Queue(reportFixture(t, dir, "Legacy delivery"))
			if err != nil {
				t.Fatal(err)
			}
			body := "<!-- llewm-source:" + entry.SourceKey + " -->"
			switch location {
			case "source":
				body = "<!-- llmem-source:" + entry.SourceKey + " -->"
			case "report":
				body += "\n<!-- llmem-report:" + entry.ID + " -->"
			case "comment":
				state.comments[1] = []string{"<!-- llmem-report:" + entry.ID + " -->"}
			}
			state.issues = []issue{{Index: 1, Body: body}}
			if _, err := adapter.Deliver(t.Context()); err == nil || !strings.Contains(err.Error(), "pre-release") {
				t.Fatalf("legacy markers not refused: %v", err)
			}
			if state.posts != 0 {
				t.Fatal("posted a duplicate before marker conversion")
			}
		})
	}
}
