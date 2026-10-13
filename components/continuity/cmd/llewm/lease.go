package main

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/lease"
)

func leaseTime(value time.Time) string { return value.UTC().Format("2006-01-02 15:04:05Z") }

func writeLeaseLine(out io.Writer, value lease.Lease) {
	when := "expires " + leaseTime(value.ExpiresAt)
	if value.ReleasedAt != nil {
		when = "released " + leaseTime(*value.ReleasedAt)
	}
	where := ""
	if value.InstanceID != "" {
		where = "\t" + value.InstanceID
	}
	fmt.Fprintf(out, "%s\ttoken %d\t%s\t%s\t%s%s\n", value.Scope(), value.Token, value.State, value.HolderLabel(), when, where)
}

// leaseWarning prints the warning of a lease kept only on this machine.
func leaseWarning(cmd *cobra.Command, result lease.Result) {
	if result.Warning != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", result.Warning)
	}
}

func leaseCommand() *cobra.Command {
	parent := &cobra.Command{
		Use:   "lease",
		Short: "Say which agent is working on a task, roadmap node, or track plan",
		Long:  "Leases are local to this machine unless config.toml [leases] remote shares them through the memory repository; then each operation reads and writes the remote, and an unreachable remote leaves advisory scopes with a local-only lease that a later sync publishes. Taking one refuses nothing by itself; checkpoint and plan writes made under a lease (--lease-holder, --lease-session, --lease-token) are stamped with its token, and scopes listed in config.toml [leases] required refuse those writes without a current lease. Each grant of a scope (task:ID, roadmap_node:ID, or track_plan:ID) carries a token that only increases. Renew before valid_until; a lapsed lease cannot be renewed, so acquire it again for a new token.",
	}
	var holder, session, output string
	var ttl time.Duration
	var token int64
	var all bool
	request := func(application *app.App, scope string) (lease.Request, error) {
		kind, id, err := lease.ParseScope(scope)
		return lease.Request{ProjectID: application.ProjectID, ScopeKind: kind, ScopeID: id, Holder: holder, SessionID: session, Token: token, TTL: ttl}, err
	}
	holderFlags := func(command *cobra.Command, withToken, withTTL bool) {
		command.Flags().StringVar(&holder, "holder", "", "holder identity, such as the client name (required)")
		command.Flags().StringVar(&session, "session", "", "holder session ID; another session of the same holder is a different holder")
		if withToken {
			command.Flags().Int64Var(&token, "token", 0, "token of the held grant (required)")
		}
		if withTTL {
			command.Flags().DurationVar(&ttl, "ttl", lease.DefaultTTL, "how long the lease lasts from now (30s to 24h)")
		}
		command.Flags().StringVar(&output, "output", "text", "text or json")
	}
	report := func(cmd *cobra.Command, verb string, value lease.Lease) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s with token %d for %s until %s; rely on it until %s.\n", verb, value.Scope(), value.Token, value.HolderLabel(), leaseTime(value.ExpiresAt), leaseTime(value.ValidUntil))
	}
	acquire := &cobra.Command{
		Use:   "acquire SCOPE",
		Short: "Take a lease when the scope is free, or renew your own",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				value, err := request(application, args[0])
				if err != nil {
					return err
				}
				result, err := application.Leases.Acquire(cmd.Context(), value)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), result)
				}
				leaseWarning(cmd, result)
				verb := "Acquired"
				if result.Renewed {
					verb = "Already held; renewed"
				}
				report(cmd, verb, result.Lease)
				return nil
			})
		},
	}
	holderFlags(acquire, false, true)
	renew := &cobra.Command{
		Use:   "renew SCOPE",
		Short: "Extend a lease you hold under the same token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				value, err := request(application, args[0])
				if err != nil {
					return err
				}
				result, err := application.Leases.Renew(cmd.Context(), value)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), result)
				}
				leaseWarning(cmd, result)
				report(cmd, "Renewed", result.Lease)
				return nil
			})
		},
	}
	holderFlags(renew, true, true)
	release := &cobra.Command{
		Use:   "release SCOPE",
		Short: "End a lease you hold so the scope is free at once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				value, err := request(application, args[0])
				if err != nil {
					return err
				}
				result, err := application.Leases.Release(cmd.Context(), value)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), result)
				}
				leaseWarning(cmd, result)
				fmt.Fprintf(cmd.OutOrStdout(), "Released %s (token %d).\n", result.Lease.Scope(), result.Lease.Token)
				return nil
			})
		},
	}
	holderFlags(release, true, false)
	list := &cobra.Command{
		Use:   "list",
		Short: "List held leases, or with --all the latest grant of every scope",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				leases, err := application.Leases.List(cmd.Context(), application.ProjectID, all)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), leases)
				}
				for _, value := range leases {
					writeLeaseLine(cmd.OutOrStdout(), value)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Leases: %d. %s\n", len(leases), lease.Disclaimer)
				return nil
			})
		},
	}
	list.Flags().BoolVar(&all, "all", false, "include expired and released leases")
	list.Flags().StringVar(&output, "output", "text", "text or json")
	show := &cobra.Command{
		Use:   "show SCOPE",
		Short: "Show a scope's grants, newest (highest token) first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			kind, id, err := lease.ParseScope(args[0])
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				history, err := application.Leases.History(cmd.Context(), application.ProjectID, kind, id, 20)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), history)
				}
				for _, value := range history {
					writeLeaseLine(cmd.OutOrStdout(), value)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Grants: %d. %s\n", len(history), lease.Disclaimer)
				return nil
			})
		},
	}
	show.Flags().StringVar(&output, "output", "text", "text or json")
	parent.AddCommand(acquire, renew, release, list, show)
	return parent
}

// leaseClaimFlags adds the flags that present the writer's lease on a
// checkpoint or plan write and returns the claim they describe.
func leaseClaimFlags(command *cobra.Command) func() lease.Claim {
	var holder, session string
	var token int64
	command.Flags().StringVar(&holder, "lease-holder", "", "holder the scope's lease was acquired under; scopes the project requires refuse the write without a current lease")
	command.Flags().StringVar(&session, "lease-session", "", "session the lease was acquired under, if any")
	command.Flags().Int64Var(&token, "lease-token", 0, "token of the lease held on the task (checkpoint) or track plan scope")
	return func() lease.Claim { return lease.Claim{Holder: holder, SessionID: session, Token: token} }
}
