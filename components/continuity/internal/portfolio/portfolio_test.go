package portfolio

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/adoption"
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/gitenv"
	"github.com/llewm/llewm/components/continuity/internal/memsync"
)

func isolated(t *testing.T) {
	t.Helper()
	t.Setenv("CONTINUITY_HOME", "")
	t.Setenv("CONTINUITY_GLOBAL_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := gitenv.Command(context.Background(), append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}

func bare(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "--quiet", "--bare")
	return root
}

func source(t *testing.T, name, lineage string) Project {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	git(t, root, "init", "--quiet")
	if _, err := config.WriteDefault(root); err != nil {
		t.Fatal(err)
	}
	db, err := database.Initialize(ctx, config.Default(root).Storage.Database)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	a, err := app.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	id, err := a.Portable.Initialize(ctx, a.ProjectID, lineage)
	if err != nil {
		t.Fatal(err)
	}
	return Project{Name: name, Root: root, ProjectID: a.ProjectID, Lineage: id.LineageID}
}

func decide(t *testing.T, p Project, id string) {
	t.Helper()
	a, err := app.Open(context.Background(), p.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, err = a.Decisions.Append(context.Background(), decision.Record{ProjectID: p.ProjectID, ID: id, Title: id, Rationale: "Test project history", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"})
	if err != nil {
		t.Fatal(err)
	}
}

func peerSync(t *testing.T, p Project, remote, clone string) {
	t.Helper()
	ctx := context.Background()
	if err := memsync.Init(ctx, remote, clone, memsync.Access{NoPrompt: true}, memsync.Setup{}); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(ctx, p.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	r, err := (memsync.Syncer{DB: a.DB, ProjectID: p.ProjectID, Remote: remote, Clone: clone, NoPrompt: true}).Sync(ctx)
	if err != nil || r.Outcome != memsync.OutcomeSynced {
		t.Fatalf("peer sync: %+v %v", r, err)
	}
}

func hasDecision(t *testing.T, p Project, id string) bool {
	t.Helper()
	a, err := app.OpenReadOnly(context.Background(), p.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var count int
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM decision_records WHERE project_id=? AND id=?`, p.ProjectID, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count == 1
}

func run(t *testing.T, c Catalog) []Result {
	t.Helper()
	r, err := Run(context.Background(), c, "", true, false)
	if err != nil {
		t.Fatalf("portfolio sync: %+v %v", r, err)
	}
	return r
}

func TestPortfolioProjectsConvergeWithoutCrossGroupHistory(t *testing.T) {
	isolated(t)
	ctx := context.Background()
	a, b := source(t, "a", ""), source(t, "b", "")
	private, alpha, beta := bare(t), bare(t), bare(t)
	c := Catalog{Version: 1, Destinations: []Destination{{Name: "private", Remote: private}, {Name: "alpha", Remote: alpha}, {Name: "beta", Remote: beta}}, Projects: []Project{a, b}}
	c.Projects[0].Routes = []Route{{"private", true}, {"alpha", false}}
	c.Projects[1].Routes = []Route{{"private", true}, {"beta", false}}
	decide(t, a, "a-local")
	decide(t, b, "b-local")
	application, err := app.Open(ctx, a.Root)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := application.Checkpoints.Create(ctx, checkpoint.Checkpoint{ProjectID: a.ProjectID, TaskID: "alpha-work", Task: "Recover alpha work", Summary: "Exact original checkpoint", NextAction: "Continue alpha"})
	application.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Preview uses no writable store, archive or clone, even for fresh routes.
	if _, err := Run(ctx, c, "", true, true); err != nil {
		t.Fatal(err)
	}
	clone, _ := config.DefaultMemoryClone(private)
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatal("preview created a clone")
	}
	if _, err := os.Stat(filepath.Join(config.StateDir(a.Root), "history")); !os.IsNotExist(err) {
		t.Fatal("preview created archive")
	}
	run(t, c)
	pa, pb := source(t, "alpha-peer", a.Lineage), source(t, "beta-peer", b.Lineage)
	ca, cb := filepath.Join(t.TempDir(), "clone"), filepath.Join(t.TempDir(), "clone")
	peerSync(t, pa, alpha, ca)
	peerSync(t, pb, beta, cb)
	if !hasDecision(t, pa, "a-local") || hasDecision(t, pa, "b-local") || !hasDecision(t, pb, "b-local") || hasDecision(t, pb, "a-local") {
		t.Fatal("project history lost or crossed group boundary")
	}
	peerApp, err := app.OpenReadOnly(ctx, pa.Root)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := peerApp.Checkpoints.Get(ctx, pa.ProjectID, cp.ID, 16384)
	peerApp.Close()
	if err != nil || exported == nil || exported.ID != cp.ID || !strings.Contains(exported.Markdown, "Continue alpha") {
		t.Fatalf("exact checkpoint not recovered: %+v %v", exported, err)
	}
	decide(t, pa, "alpha-peer-change")
	decide(t, pb, "beta-peer-change")
	peerSync(t, pa, alpha, ca)
	peerSync(t, pb, beta, cb)
	run(t, c)
	if !hasDecision(t, a, "alpha-peer-change") || hasDecision(t, a, "beta-peer-change") || !hasDecision(t, b, "beta-peer-change") {
		t.Fatal("group changes did not return to the correct project")
	}
	// A private-only contributor learns A through the aggregate. Its new records
	// reach A locally but must not be forwarded to Alpha without Alpha's relay.
	pp := source(t, "private-peer", a.Lineage)
	pc := filepath.Join(t.TempDir(), "clone")
	peerSync(t, pp, private, pc)
	if !hasDecision(t, pp, "alpha-peer-change") || hasDecision(t, pp, "b-local") {
		t.Fatal("aggregate failed lineage-scoped round trip")
	}
	decide(t, pp, "private-only-a")
	peerSync(t, pp, private, pc)
	run(t, c)
	peerSync(t, pa, alpha, ca)
	if !hasDecision(t, a, "private-only-a") || hasDecision(t, pa, "private-only-a") {
		t.Fatal("received private record bypassed relay consent")
	}
	c.Projects[0].Routes[1].Relay = true
	run(t, c)
	peerSync(t, pa, alpha, ca)
	if !hasDecision(t, pa, "private-only-a") {
		t.Fatal("explicit relay did not forward retained history")
	}
	for _, pair := range []struct{ remote, forbidden string }{{alpha, b.Lineage}, {beta, a.Lineage}} {
		// Check every committed path, not merely the final working tree.
		history := git(t, pair.remote, "log", "--all", "--format=", "--name-only")
		if strings.Contains(history, strings.TrimPrefix(pair.forbidden, "lineage:")) {
			t.Fatal("another project's lineage entered group Git history")
		}
	}
	for _, r := range run(t, c) {
		for _, d := range r.Destinations {
			for _, attempt := range d.Attempts {
				if attempt.ExportedRecords != 0 || attempt.ImportedRecords != 0 || attempt.UnpushedCommits != 0 || attempt.Collisions != 0 {
					t.Fatalf("replay did not settle: %+v", attempt)
				}
			}
		}
	}
}

func TestPortfolioPinFailuresAndIndependentEndpointFailures(t *testing.T) {
	isolated(t)
	p := source(t, "p", "")
	good, bad := bare(t), filepath.Join(t.TempDir(), "absent.git")
	p.Routes = []Route{{"bad", false}, {"good", false}}
	c := Catalog{Version: 1, Destinations: []Destination{{Name: "bad", Remote: bad}, {Name: "good", Remote: good}}, Projects: []Project{p}}
	decide(t, p, "survives-failure")
	results, err := Run(context.Background(), c, "p", false, false)
	if err == nil || len(results) != 1 || results[0].Outcome != "partial" || results[0].Destinations[1].Outcome != "synced" {
		t.Fatalf("independent failure lost receipts/success: %+v %v", results, err)
	}
	if !strings.Contains(git(t, good, "ls-tree", "-r", "--name-only", "HEAD"), strings.TrimPrefix(p.Lineage, "lineage:")) {
		t.Fatal("successful endpoint did not receive project")
	}
	status, err := ReadStatus(context.Background(), c, "p", false)
	if err != nil || status[0].Destinations[0].Outcome != "error" || status[0].Destinations[1].Outcome != "synced" {
		t.Fatalf("endpoint-specific failures not retained: %+v %v", status, err)
	}
	c.Projects[0].ProjectID = "project-wrong"
	results, err = Run(context.Background(), c, "p", false, false)
	if err == nil || len(results[0].Destinations) != 0 {
		t.Fatal("identity drift reached a destination")
	}
	c.Projects[0] = p
	c.Projects[0].Lineage = "lineage:" + strings.Repeat("1", 32)
	if _, err := Run(context.Background(), c, "p", false, true); err == nil {
		t.Fatal("lineage drift passed preview")
	}
	c.Projects[0] = p
	if err := adoption.Save(config.StateDir(p.Root), adoption.State{Version: 1, History: []adoption.Transition{{State: "trial"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), c, "p", false, false); err == nil {
		t.Fatal("portfolio bypassed trial gate")
	}
}

func TestPortfolioCatalogValidationAndAtomicEdits(t *testing.T) {
	isolated(t)
	store := Store{Directory: filepath.Join(t.TempDir(), "config")}
	if c, err := store.Load(); err != nil || c.Version != 1 {
		t.Fatal(c, err)
	}
	if _, err := os.Stat(store.Directory); !os.IsNotExist(err) {
		t.Fatal("read created configuration")
	}
	if _, err := store.Update(func(c *Catalog) error {
		c.Destinations = []Destination{{Name: "private", Remote: "/absolute/private.git"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(store.Directory, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(func(c *Catalog) error { c.Destinations = append(c.Destinations, c.Destinations[0]); return nil }); err == nil {
		t.Fatal("accepted duplicate destination")
	}
	after, _ := os.ReadFile(filepath.Join(store.Directory, FileName))
	if string(before) != string(after) {
		t.Fatal("invalid edit changed catalog")
	}
	for _, remote := range []string{"relative.git", "-u", "https://user:secret@host/repo", "https://host/repo?token=secret", "ssh://host/repo#fragment", "ext::arbitrary command", "a\nb"} {
		if ValidateRemote(remote) == nil {
			t.Fatalf("accepted unsafe remote %q", remote)
		}
	}
	for _, remote := range []string{"/absolute/repo.git", "git@host:group/repo.git", "ssh://git@host/group/repo.git", "https://host/group/repo.git"} {
		if err := ValidateRemote(remote); err != nil {
			t.Fatalf("rejected remote %q: %v", remote, err)
		}
	}
	p := source(t, "p", "")
	p.Routes = []Route{{"private", false}}
	q := p
	q.Name, q.Root, q.ProjectID = "q", t.TempDir(), "project-other"
	if (Catalog{Version: 1, Destinations: []Destination{{Name: "private", Remote: "/absolute/private.git"}}, Projects: []Project{p, q}}).Validate() == nil {
		t.Fatal("same lineage presented as independent project")
	}
	if err := os.WriteFile(filepath.Join(store.Directory, "portfolio.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(func(*Catalog) error { return nil }); err == nil {
		t.Fatal("concurrent update bypassed catalog lock")
	}
}

func TestPortfolioRejectsStorageOverlapThroughMissingSymlinkDescendants(t *testing.T) {
	isolated(t)
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := separate(filepath.Join(link, "missing", "clone"), root); err == nil {
		t.Fatal("prospective clone escaped overlap check through symlink")
	}
	if err := separate(filepath.Join(t.TempDir(), "missing", "clone"), root); err != nil {
		t.Fatal(err)
	}
}

func TestPortfolioRefusesForeignCloneBeforePublishing(t *testing.T) {
	isolated(t)
	p := source(t, "p", "")
	decide(t, p, "must-not-publish")
	intended, other := bare(t), bare(t)
	clone := filepath.Join(t.TempDir(), "clone")
	if err := memsync.Init(context.Background(), other, clone, memsync.Access{}, memsync.Setup{}); err != nil {
		t.Fatal(err)
	}
	p.Routes = []Route{{"group", true}}
	c := Catalog{Version: 1, Destinations: []Destination{{Name: "group", Remote: intended, Clone: clone}}, Projects: []Project{p}}
	if _, err := Run(context.Background(), c, "p", false, false); err == nil {
		t.Fatal("used a clone owned by another destination")
	}
	for _, remote := range []string{intended, other} {
		if refs := git(t, remote, "for-each-ref", "--format=%(refname)"); refs != "" {
			t.Fatal("refusing a foreign clone still published history")
		}
	}
}
