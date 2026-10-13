package deliverystatus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const statusFixture = `{"schema_version":1,"observed_at":"2026-10-10T12:00:00Z","authority":"machine_observed","reports":[{"project_id":"p","id":"a","report_id":"sha256:a","source_ref":"work_item:bug","target":"https://forge.invalid/owner/repo?token=secret-value","status":"delivered","queued_at":"2026-10-10T11:00:00Z","receipt":{"issue":1,"url":"https://forge.invalid/owner/repo/issues/1","outcome":"created","recorded_at":"2026-10-10T11:30:00Z"}},{"project_id":"other","id":"b","report_id":"sha256:b","source_ref":"work_item:other","target":"private-target","status":"pending","queued_at":"2026-10-10T11:00:00Z"}]}`

func TestReadSnapshotScopesRedactsAndRequiresProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	for _, body := range []string{statusFixture, strings.Replace(statusFixture, `"schema_version":1`, `"schema_version":2`, 1), strings.Replace(statusFixture, `"machine_observed"`, `"caller_asserted"`, 1), strings.Replace(statusFixture, `"status":"delivered"`, `"status":"invented"`, 1), strings.Repeat(" ", MaxBytes+1)} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := Read(path, "p")
		if body != statusFixture {
			if err == nil {
				t.Fatalf("accepted invalid snapshot")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Reports) != 1 || got.Reports[0].ProjectID != "p" || strings.Contains(got.Reports[0].Target, "secret-value") {
			t.Fatalf("unscoped/unredacted: %+v", got)
		}
	}
	if got, err := Read("", "p"); err != nil || got != nil {
		t.Fatalf("unsupplied: %+v %v", got, err)
	}
}
