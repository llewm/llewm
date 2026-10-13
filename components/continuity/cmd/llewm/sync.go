package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/memsync"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

func syncCommand() *cobra.Command {
	var output string
	var allowPrompt bool
	parent := &cobra.Command{
		Use:   "sync",
		Short: "Sync curated history with the project's memory repository",
		Long:  "Export new records, commit, rebase onto the remote, import what arrived, and push. When the remote is unreachable the local commit is kept and the push is retried next time.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				syncer, err := projectSyncer(application)
				if err != nil {
					return err
				}
				syncer.NoPrompt = !allowPrompt
				result, err := syncer.Sync(cmd.Context())
				if format == "json" {
					if writeErr := writeJSON(cmd.OutOrStdout(), result); writeErr != nil {
						return writeErr
					}
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "Sync %s: exported %d, imported %d, unpushed commits %d\n%s\n", result.Outcome, result.ExportedRecords, result.ImportedRecords, result.UnpushedCommits, result.Detail)
					for _, note := range result.LeaseNotes {
						fmt.Fprintf(cmd.OutOrStdout(), "lease: %s\n", note)
					}
				}
				if err == nil && result.Outcome == memsync.OutcomeCollision {
					return errors.New("sync stopped: imported records collide with local records")
				}
				return err
			})
		},
	}
	parent.Flags().BoolVar(&allowPrompt, "prompt", false, "allow configured askpass for this explicit sync; default retains pending records without prompting")
	parent.Flags().StringVar(&output, "output", "text", "text or json")

	var convertOutput string
	convert := &cobra.Command{
		Use:   "convert",
		Short: "Convert this lineage's format-2 observations to format 3 and sync",
		Long: `Run one sync that also converts this lineage's format-2 observations (v2/) in the
memory repository in one Continuity-made commit and pushes it. Run it once, on
one machine, after every machine that syncs this lineage runs a format-3 build;
until then, ordinary sync refuses a repository with format-2 observations.
Original observations remain in Git history; payload and record identities are
preserved. Unknown or invalid records refuse conversion before removal.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(convertOutput)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				syncer, err := projectSyncer(application)
				if err != nil {
					return err
				}
				result, err := syncer.Convert(cmd.Context())
				if format == "json" {
					if writeErr := writeJSON(cmd.OutOrStdout(), result); writeErr != nil {
						return writeErr
					}
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "Convert %s: exported %d, imported %d, unpushed commits %d\n%s\n", result.Outcome, result.ExportedRecords, result.ImportedRecords, result.UnpushedCommits, result.Detail)
				}
				return err
			})
		},
	}
	convert.Flags().StringVar(&convertOutput, "output", "text", "text or json")
	parent.AddCommand(convert)

	var remote, clone, askpass, sshKey, credentialHelper, lineageID string
	var plaintextCredentials, tokenStdin bool
	initialize := &cobra.Command{
		Use:   "init",
		Short: "Give this project a portable identity, and with --remote name its memory repository and clone it",
		Long: `Without --remote, create the project's portable lineage, or adopt the one named
by --lineage, and print the identity; an existing identity is never replaced.
With --remote, also clone the memory repository and configure sync. A project
without an identity then joins --lineage, or starts a new history when the
memory repository holds none.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(remote) == "" {
				return withApp(cmd, func(application *app.App) error {
					value, err := application.Portable.Initialize(cmd.Context(), application.ProjectID, lineageID)
					if err != nil {
						return err
					}
					return writeJSON(cmd.OutOrStdout(), value)
				})
			}
			return withApp(cmd, func(application *app.App) error {
				target := clone
				if target == "" {
					var err error
					if target, err = memsync.DefaultClone(remote); err != nil {
						return err
					}
				} else if abs, err := filepath.Abs(target); err == nil {
					target = abs
				}
				settings := application.Config.Sync
				settings.Remote, settings.Clone = remote, clone
				if cmd.Flags().Changed("askpass") {
					if askpass != "" && !filepath.IsAbs(askpass) {
						return errors.New("--askpass must be an absolute path")
					}
					settings.Askpass = askpass
				}
				if cmd.Flags().Changed("ssh-key") {
					if sshKey != "" && !filepath.IsAbs(sshKey) {
						return errors.New("--ssh-key must be an absolute path")
					}
					settings.SSHKey = sshKey
				}
				access := memsync.Access{Askpass: settings.Askpass, SSHKey: settings.SSHKey}
				var token string
				if tokenStdin {
					if !memsync.HTTPRemote(remote) {
						return errors.New("--token-stdin applies to HTTPS remotes")
					}
					line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
					if err != nil && !errors.Is(err, io.EOF) {
						return err
					}
					if token = strings.TrimSpace(line); token == "" {
						return errors.New("--token-stdin read no token")
					}
				}
				// Git prompts only when run from a terminal and no token was given.
				interactive := !tokenStdin && term.IsTerminal(int(os.Stdin.Fd()))
				helper, err := memoryCredentialHelper(cmd, remote, target, access, credentialHelper, plaintextCredentials)
				if err != nil {
					return err
				}
				if err := memsync.Init(cmd.Context(), remote, target, access, memsync.Setup{Helper: helper, Interactive: interactive, Token: token}); err != nil {
					return err
				}
				if err := memsync.CheckAccess(cmd.Context(), target, access); err != nil {
					return fmt.Errorf("the memory repository did not answer without a prompt, which syncs from session hooks require (%w). If the credential helper needs an unlocked keyring, unlock it or choose another with --credential-helper; an SSH remote with --ssh-key also works; storing the password unencrypted with --plaintext-credentials is the last resort", err)
				}
				identity, err := (portable.Service{DB: application.DB}).Get(cmd.Context(), application.ProjectID)
				if err != nil {
					return err
				}
				if identity == nil {
					lineages := cloneLineages(target)
					if lineageID == "" && len(lineages) > 0 {
						return fmt.Errorf("this project has no portable identity yet and the memory repository holds other histories; rerun sync init with --lineage LINEAGE to join one (%s), or run `llewm sync init` without --remote first to start a separate history", strings.Join(lineages, ", "))
					}
					created, err := application.Portable.Initialize(cmd.Context(), application.ProjectID, lineageID)
					if err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Lineage: %s (%s)\n", created.LineageID, created.AssociationSource)
				} else if lineageID != "" && lineageID != identity.LineageID {
					return fmt.Errorf("this project already belongs to %s, not %s", identity.LineageID, lineageID)
				}
				path, err := config.SetSync(application.Root, settings)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Memory repository: %s\nClone: %s\nConfig: %s\nRun `llewm sync` to sync now.\n", redact.String(remote), target, path)
				return nil
			})
		},
	}
	initialize.Flags().StringVar(&remote, "remote", "", "memory repository Git URL; omit to set up only the portable identity")
	initialize.Flags().StringVar(&lineageID, "lineage", "", "explicit lineage:<32 lowercase hex> to adopt or join; omit to create one")
	initialize.Flags().StringVar(&clone, "clone", "", "local clone path (default: shared per remote under the user data directory)")
	initialize.Flags().StringVar(&sshKey, "ssh-key", "", "private key for this project's syncs over SSH, used instead of the SSH configuration's identities (empty clears it)")
	initialize.Flags().StringVar(&credentialHelper, "credential-helper", "", "Git credential helper to configure in the memory clone only, such as libsecret (default: your existing helper, else libsecret when installed)")
	initialize.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read an HTTPS access token from standard input and hand it to the credential helper instead of prompting (the user name comes from the remote URL)")
	initialize.Flags().BoolVar(&plaintextCredentials, "plaintext-credentials", false, "last resort: let Git's store helper keep the HTTPS password unencrypted beside the clone; asks for confirmation")
	initialize.Flags().StringVar(&askpass, "askpass", "", "fallback credential prompt program for this project's syncs, used when no credential helper, GIT_ASKPASS, or core.askPass answers (empty clears it)")

	var statusOutput string
	status := &cobra.Command{
		Use:   "status",
		Short: "Show the latest sync attempt and what has not been pushed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(statusOutput)
			if err != nil {
				return err
			}
			return withReadOnlyApp(cmd, func(application *app.App) error {
				value, err := projectSyncStatus(cmd, application)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), value)
				}
				line := value.Line(time.Now().UTC())
				if line == "" {
					line = "Sync: not configured (llewm sync init --remote URL)"
				}
				fmt.Fprintln(cmd.OutOrStdout(), line)
				if value.Identity == nil {
					fmt.Fprintln(cmd.OutOrStdout(), "Identity: none (llewm sync init)")
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "Identity: %s · instance %s\n", value.Identity.LineageID, value.Identity.InstanceID)
				}
				return nil
			})
		},
	}
	status.Flags().StringVar(&statusOutput, "output", "text", "text or json")

	parent.AddCommand(initialize, status)
	return parent
}

// memoryCredentialHelper chooses the credential helper `sync init` configures in
// the memory clone. Continuity never handles the secret: Git prompts for it and
// the helper stores it. In order of preference: an explicit helper, a helper the
// clone or the user's Git configuration already has, libsecret when installed,
// and only on explicit, confirmed request, Git's plaintext store helper.
// Wallet-backed askpass may retrieve saved credentials, but automatic sync
// disables askpass because it can also prompt. Setup needs an access path that
// works with askpass disabled; CheckAccess verifies it before saving config.
func memoryCredentialHelper(cmd *cobra.Command, remote, clone string, access memsync.Access, explicit string, plaintext bool) (string, error) {
	out := cmd.ErrOrStderr()
	if !memsync.HTTPRemote(remote) {
		if explicit != "" || plaintext {
			return "", errors.New("credential helpers apply to HTTPS remotes; an SSH remote authenticates with your SSH agent or --ssh-key")
		}
		return "", nil
	}
	if explicit != "" && plaintext {
		return "", errors.New("choose either --credential-helper or --plaintext-credentials")
	}
	if explicit != "" {
		fmt.Fprintf(out, "Credentials: Git will prompt if needed and the %q credential helper will keep them. It is configured in the memory clone only.\n", explicit)
		return explicit, nil
	}
	if plaintext {
		file := memsync.PlaintextCredentialFile(clone)
		fmt.Fprintf(out, "Last resort: Git's store helper will keep the password for %s UNENCRYPTED in\n  %s\n"+
			"(readable only by you). Anyone who can read your files can read it. Prefer a keyring\n"+
			"helper (--credential-helper libsecret), an SSH remote (--ssh-key), or an access token\n"+
			"scoped to this repository that you can revoke.\n", redact.String(remote), file)
		// Confirm on the terminal even when standard input carries a token.
		if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
			defer tty.Close()
			fmt.Fprint(tty, "Type yes to store it unencrypted: ")
			answer, err := bufio.NewReader(tty).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return "", err
			}
			if strings.TrimSpace(answer) != "yes" {
				return "", errors.New("plaintext credentials not confirmed; nothing was stored")
			}
		} else {
			fmt.Fprintln(out, "No terminal to confirm on: --plaintext-credentials is taken as explicit consent.")
		}
		return memsync.PlaintextHelper(clone), nil
	}
	if existing := memsync.Helper(cmd.Context(), clone); existing != "" {
		fmt.Fprintf(out, "Credentials: keeping the memory clone's %q credential helper.\n", existing)
		return "", nil
	}
	if existing := memsync.UserHelper(cmd.Context(), remote); existing != "" {
		fmt.Fprintf(out, "Credentials: using your existing Git credential helper (%q); nothing is configured.\n", existing)
		return "", nil
	}
	if memsync.LibsecretAvailable(cmd.Context()) {
		fmt.Fprintln(out, "Credentials: Git will prompt if needed and libsecret will keep them in your keyring. It is configured in the memory clone only.")
		return "libsecret", nil
	}
	if access.Askpass != "" {
		return "", errors.New("askpass can obtain credentials for setup but automatic sync disables it; install libsecret with a Secret Service keyring, name a persistent helper with --credential-helper, or use an SSH remote with --ssh-key")
	}
	return "", errors.New("no Git credential helper is available for this HTTPS remote. Install libsecret with a Secret Service keyring and rerun, name a helper with --credential-helper, or use an SSH remote with --ssh-key. As a last resort, --plaintext-credentials stores the password unencrypted beside the clone")
}

func projectSyncer(application *app.App) (memsync.Syncer, error) {
	settings := application.Config.Sync
	if settings.Remote == "" {
		return memsync.Syncer{}, errors.New("no memory repository configured; run `llewm sync init --remote URL`")
	}
	clone := settings.Clone
	if clone == "" {
		var err error
		if clone, err = memsync.DefaultClone(settings.Remote); err != nil {
			return memsync.Syncer{}, err
		}
	}
	syncer := memsync.Syncer{DB: application.DB, ProjectID: application.ProjectID, Remote: settings.Remote, Clone: clone, Askpass: settings.Askpass, SSHKey: settings.SSHKey}
	if application.Config.Leases.Remote {
		leases := application.Leases
		leases.Remote = nil
		syncer.Leases = &leases
	}
	return syncer, nil
}

// enableCrossMachineLeases makes the app's lease operations read and write the
// memory remote when the project sets [leases] remote.
func enableCrossMachineLeases(application *app.App) {
	if !application.Config.Leases.Remote {
		return
	}
	if syncer, err := projectSyncer(application); err == nil {
		application.Leases.Remote = syncer.LeaseRemote()
	}
}

func projectSyncStatus(cmd *cobra.Command, application *app.App) (memsync.Status, error) {
	value, err := memsync.ReadStatus(cmd.Context(), application.DB, application.ProjectID)
	if err != nil {
		return value, err
	}
	if value.Identity, err = application.Portable.Get(cmd.Context(), application.ProjectID); err != nil {
		return value, err
	}
	if application.Config.Sync.Remote != "" {
		syncer, err := projectSyncer(application)
		if err != nil {
			return value, err
		}
		value.Configured, value.Remote, value.Clone = true, redact.String(syncer.Remote), syncer.Clone
		value.Settings = syncSettings(cmd.Context(), application.Config.Sync, syncer.Clone)
	}
	return value, nil
}

// syncSettings names every deliberate sync setting that differs from the
// default, including the memory clone's own credential helper.
func syncSettings(ctx context.Context, settings config.Sync, clone string) []string {
	var values []string
	if settings.Manual {
		values = append(values, "automatic sync off (manual = true)")
	}
	if settings.SessionTimeoutSeconds > 0 {
		values = append(values, fmt.Sprintf("session sync limit %s", settings.SessionTimeout()))
	}
	if settings.Clone != "" {
		values = append(values, "clone "+settings.Clone)
	}
	if settings.SSHKey != "" {
		values = append(values, "SSH key "+settings.SSHKey)
	}
	if settings.Askpass != "" {
		values = append(values, "askpass "+settings.Askpass)
	}
	switch helper := memsync.Helper(ctx, clone); {
	case helper == memsync.PlaintextHelper(clone):
		values = append(values, "PLAINTEXT credentials in "+memsync.PlaintextCredentialFile(clone))
	case helper != "":
		values = append(values, "clone credential helper "+helper)
	}
	return values
}

// cloneLineages lists the lineages present in a memory clone.
func cloneLineages(clone string) []string {
	entries, err := os.ReadDir(filepath.Join(clone, portable.MemoryRepositoryRoot))
	if err != nil {
		return nil
	}
	var lineages []string
	for _, entry := range entries {
		if entry.IsDir() {
			lineages = append(lineages, "lineage:"+entry.Name())
		}
	}
	sort.Strings(lineages)
	return lineages
}

// startBackgroundSync is replaced in tests so they never spawn processes.
var startBackgroundSync = memsync.StartBackground

// enableAutomaticSync pushes in the background after each new checkpoint when
// the project syncs automatically.
func enableAutomaticSync(application *app.App) {
	if !application.Config.Sync.Automatic() {
		return
	}
	root := application.Root
	application.AfterCheckpoint = func() {
		if executable, err := os.Executable(); err == nil {
			_ = startBackgroundSync(executable, root)
		}
	}
}

// sessionStartSync fetches and imports before the session bootstrap, waiting on
// the remote only within the session budget, and returns the sync status line.
// A push, or a fetch that did not fit the budget, continues in a background
// sync; the attempt is recorded either way.
func sessionStartSync(ctx context.Context, application *app.App) string {
	settings := application.Config.Sync
	if settings.Remote == "" {
		return ""
	}
	if settings.Automatic() {
		if syncer, err := projectSyncer(application); err == nil {
			result, _ := syncer.Session(ctx, settings.SessionTimeout())
			if result.Outcome == memsync.OutcomeDeferred {
				if executable, err := os.Executable(); err == nil {
					_ = startBackgroundSync(executable, application.Root)
				}
			}
		}
	}
	value, err := memsync.ReadStatus(ctx, application.DB, application.ProjectID)
	if err != nil {
		return ""
	}
	value.Configured = true
	if syncer, err := projectSyncer(application); err == nil {
		value.Settings = syncSettings(ctx, settings, syncer.Clone)
	}
	return value.Line(time.Now().UTC())
}
