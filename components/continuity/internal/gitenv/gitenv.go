// Package gitenv starts Git subprocesses in an environment that cannot point
// them at a repository other than the one Continuity names with -C.
package gitenv

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// repositoryEnv lists the variables that select a repository, index, or object
// store, or inject configuration: Git's `rev-parse --local-env-vars`, plus
// namespace and quarantine. A Git hook or wrapper that runs Continuity sets some
// of them; inherited, they would redirect Continuity's Git commands into another
// repository, so they are always dropped.
var repositoryEnv = map[string]bool{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_CONFIG": true, "GIT_CONFIG_PARAMETERS": true,
	"GIT_CONFIG_COUNT": true, "GIT_OBJECT_DIRECTORY": true, "GIT_DIR": true, "GIT_WORK_TREE": true,
	"GIT_IMPLICIT_WORK_TREE": true, "GIT_GRAFT_FILE": true, "GIT_INDEX_FILE": true,
	"GIT_NO_REPLACE_OBJECTS": true, "GIT_REPLACE_REF_BASE": true, "GIT_PREFIX": true,
	"GIT_SHALLOW_FILE": true, "GIT_COMMON_DIR": true, "GIT_NAMESPACE": true, "GIT_QUARANTINE_PATH": true,
}

// Environ is the process environment without repository-selecting and
// configuration-injecting Git variables. User-level choices such as credential
// programs, SSH commands, proxies, global config, and tracing pass through.
func Environ() []string {
	var env []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if repositoryEnv[name] || strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
			continue
		}
		env = append(env, entry)
	}
	return env
}

// Command returns a Git command with args and the isolated environment.
func Command(ctx context.Context, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, "git", args...)
	command.Env = Environ()
	return command
}
