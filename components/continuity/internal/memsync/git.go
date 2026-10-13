package memsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
)

// git runs Git in the memory clone without prompts, hooks, or signing, so a
// sync can never block on interaction or run repository-provided code.
type git struct {
	Dir string
	// Identity is extra -c options naming a fallback committer when the user has
	// no Git identity configured.
	Identity []string
	// Askpass is a fallback prompt program for remote credentials. It is passed
	// as SSH_ASKPASS, the last program Git consults, so credential helpers,
	// GIT_ASKPASS, and core.askPass all still take precedence over it.
	Askpass string
	// SSHKey is a private key used for SSH remotes instead of the user's SSH
	// configuration choice of identity.
	SSHKey string
	// Helper, when set, replaces every inherited credential helper for this
	// command. Only sync init uses it, for the clone that precedes the clone's
	// own configuration.
	Helper string
	// Interactive lets Git and SSH prompt on the terminal. Only an explicit
	// `sync init` run from a terminal sets it; syncs from hooks never do.
	Interactive bool
	// NoPrompt disables askpass as well as terminal interaction for automatic sync.
	NoPrompt bool
}

// runInput runs Git with input on standard input and discards its output.
func (g git) runInput(ctx context.Context, input string, args ...string) error {
	return g.execOut(ctx, strings.NewReader(input), &bytes.Buffer{}, args)
}

func (g git) run(ctx context.Context, args ...string) (string, error) {
	var stdout bytes.Buffer
	err := g.execOut(ctx, nil, &stdout, args)
	return strings.TrimSpace(stdout.String()), err
}

func (g git) execOut(ctx context.Context, stdin io.Reader, stdout *bytes.Buffer, args []string) error {
	base := append([]string{"-C", g.Dir}, g.config()...)
	command := gitenv.Command(ctx, append(base, args...)...)
	if !g.Interactive || g.NoPrompt {
		// A separate process group cannot read the terminal, so a Git that may
		// prompt keeps the default.
		stopGently(command)
	}
	command.WaitDelay = 3 * time.Second
	command.Env = g.env()
	command.Stdin = stdin
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = stdout, &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if g.NoPrompt && strings.Contains(detail, "terminal prompts disabled") {
			detail = "authentication unavailable: automatic sync cannot prompt; configure a credential helper or SSH key with llewm sync init"
		}
		if detail == "" {
			detail = err.Error()
		}
		return &gitError{Args: args, Detail: detail, Err: err}
	}
	return nil
}

// config is the -c options every command runs with.
func (g git) config() []string {
	options := []string{
		"-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "rebase.autoStash=false",
		// Nothing may rewrite envelope bytes, start a monitor program, or run
		// maintenance inside the bounded cycle.
		"-c", "core.autocrlf=false", "-c", "core.fsmonitor=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false",
		// An HTTP transfer below 1 KB/s for 20 s has stalled; end it rather than
		// wait out the sync's whole network limit.
		"-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=20",
	}
	if g.NoPrompt {
		options = append(options, "-c", "core.askPass=")
	}
	if g.Helper != "" {
		// The empty value clears helpers inherited from other configuration.
		options = append(options, "-c", "credential.helper=", "-c", "credential.helper="+g.Helper)
	}
	return append(options, g.Identity...)
}

// shellQuote quotes value for the shell Git runs helpers and SSH commands in.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// env is the process environment for Git: no terminal prompts and batch-mode
// SSH unless Interactive, the configured key or else the user's SSH command, and
// the fallback askpass when set.
func (g git) env() []string {
	env := credentialSessionEnv(gitenv.Environ())
	batch := ""
	if !g.Interactive || g.NoPrompt {
		env = append(env, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
		// Fail fast on an unreachable host and notice a connection that died.
		batch = " -o BatchMode=yes -o ConnectTimeout=5 -o ServerAliveInterval=5 -o ServerAliveCountMax=3"
	}
	if g.SSHKey != "" {
		// A configured key replaces any ambient GIT_SSH_COMMAND.
		env = append(env, "GIT_SSH_COMMAND=ssh -i "+shellQuote(g.SSHKey)+" -o IdentitiesOnly=yes"+batch)
	} else if os.Getenv("GIT_SSH_COMMAND") == "" && batch != "" {
		env = append(env, "GIT_SSH_COMMAND=ssh"+batch)
	}
	if g.NoPrompt {
		// Empty GIT_ASKPASS prevents fallback to configured core.askPass; clear
		// SSH fallback too. Credential helpers may still return cached credentials.
		env = append(env, "GIT_ASKPASS=", "SSH_ASKPASS=", "SSH_ASKPASS_REQUIRE=never")
	} else if g.Askpass != "" {
		// Later duplicates win in os/exec, replacing any ambient SSH_ASKPASS.
		env = append(env, "SSH_ASKPASS="+g.Askpass)
	}
	return env
}

type gitError struct {
	Args   []string
	Detail string
	Err    error
}

func (e *gitError) Error() string {
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), e.Detail)
}

func (e *gitError) Unwrap() error { return e.Err }

// exitCode reports the process exit code of a failed Git command, or -1.
func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}
