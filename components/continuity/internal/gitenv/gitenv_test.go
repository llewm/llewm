package gitenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEnvironDropsRepositorySelection(t *testing.T) {
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_NAMESPACE", "GIT_QUARANTINE_PATH", "GIT_COMMON_DIR"} {
		t.Setenv(name, "x")
	}
	t.Setenv("GIT_TRACE", "0")
	t.Setenv("GIT_SSH_COMMAND", "ssh")
	kept := map[string]bool{}
	for _, entry := range Environ() {
		name, _, _ := strings.Cut(entry, "=")
		kept[name] = true
	}
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_NAMESPACE", "GIT_QUARANTINE_PATH", "GIT_COMMON_DIR"} {
		if kept[name] {
			t.Errorf("%s was inherited", name)
		}
	}
	if !kept["GIT_TRACE"] || !kept["GIT_SSH_COMMAND"] {
		t.Error("user-level Git variables were dropped")
	}
}

// TestLocalEnvVarsCovered keeps the list in step with the installed Git.
func TestLocalEnvVarsCovered(t *testing.T) {
	out, err := exec.Command("git", "rev-parse", "--local-env-vars").Output()
	if err != nil {
		t.Skip("git unavailable:", err)
	}
	for _, name := range strings.Fields(string(out)) {
		if !repositoryEnv[name] {
			t.Errorf("Git lists %s as repository-local but it is not dropped", name)
		}
	}
}

func TestCommandIgnoresInheritedGitDir(t *testing.T) {
	target, decoy := t.TempDir(), t.TempDir()
	for _, dir := range []string{target, decoy} {
		if output, err := exec.Command("git", "init", "--quiet", dir).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, output)
		}
	}
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	out, err := Command(context.Background(), "-C", target, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(target, ".git"))
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if got != want {
		t.Fatalf("git dir %s, want %s", got, want)
	}
	os.Unsetenv("GIT_DIR")
}

// TestNoDirectGitCommands keeps every Git subprocess in the module on the
// isolated environment.
func TestNoDirectGitCommands(t *testing.T) {
	direct := regexp.MustCompile(`exec\.Command(Context)?\((ctx, )?"git"`)
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, filepath.Join("gitenv", "gitenv.go")) {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if direct.Match(content) {
			t.Errorf("%s starts Git directly; use gitenv.Command", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
