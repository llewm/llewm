package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/fork"
	"github.com/llewm/llewm/components/continuity/internal/lease"
)

const forkDisclaimer = "Forks are machine-observed, usually from two machines writing before syncing: a task with several checkpoint heads, a record revision (TYPE:ID@REVISION) with several versions, a decision or narrative (FAMILY:ID) superseded more than once, or a stale write (stale:KIND:ID) made under a lease token after a newer grant existed. Every head stays in history; resolving continues from the accepted head."

func staleSubject(ref string) bool {
	return strings.HasPrefix(strings.TrimSpace(ref), lease.StaleSubjectPrefix)
}

func writeStaleLine(out io.Writer, value lease.StaleWrite) {
	fmt.Fprintf(out, "%s\ttoken %d of %s\tafter token %d of %s\tproposed lease\n", value.Ref(), value.Stamp.Token, value.Stamp.Scope(), value.Newer.Token, value.Newer.HolderLabel())
}

// forkTask accepts task:ID or a bare task ID.
func forkTask(ref string) (string, error) {
	task := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), "task:"))
	if task == "" {
		return "", errors.New("name the forked task as task:ID")
	}
	return task, nil
}

// revisionForkRef reports whether ref names a record revision
// (TYPE:ID@REVISION) rather than a task.
func revisionForkRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "task:") {
		return false
	}
	_, _, _, err := fork.ParseRevisionSubject(ref)
	return err == nil
}

// noRevisionFork explains that subject is not an open revision fork and names
// the open ones, since the type is easy to misname (topic_revision, not topic).
func noRevisionFork(cmd *cobra.Command, application *app.App, subject string) error {
	forks, err := fork.RevisionForks(cmd.Context(), application.DB, application.ProjectID, "")
	if err != nil || len(forks) == 0 {
		return fmt.Errorf("%s has no unresolved fork", subject)
	}
	refs := make([]string, len(forks))
	for index, value := range forks {
		refs[index] = value.Ref()
	}
	return fmt.Errorf("%s has no unresolved fork; open revision forks: %s", subject, strings.Join(refs, ", "))
}

func shortHash(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func revisionHeadLabel(head fork.RevisionHead) string {
	label := shortHash(head.ContentSHA256)
	if head.Local {
		label += " (held here)"
	}
	return label
}

func forkCommand() *cobra.Command {
	parent := &cobra.Command{Use: "fork", Short: "List, inspect, and resolve records with several heads"}
	var output string
	list := &cobra.Command{
		Use:   "list",
		Short: "List tasks, record revisions, and superseded records with several heads",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				forks, err := fork.CheckpointForks(cmd.Context(), application.DB, application.ProjectID, "")
				if err != nil {
					return err
				}
				revisions, err := fork.RevisionForks(cmd.Context(), application.DB, application.ProjectID, "")
				if err != nil {
					return err
				}
				supersessions, err := fork.SupersessionForks(cmd.Context(), application.DB, application.ProjectID, "")
				if err != nil {
					return err
				}
				stale, err := lease.StaleWrites(cmd.Context(), application.DB, application.ProjectID, "")
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), struct {
						Forks             []fork.Fork             `json:"forks"`
						RevisionForks     []fork.RevisionFork     `json:"revision_forks"`
						SupersessionForks []fork.SupersessionFork `json:"supersession_forks"`
						StaleWrites       []lease.StaleWrite      `json:"stale_writes"`
					}{append([]fork.Fork{}, forks...), append([]fork.RevisionFork{}, revisions...), append([]fork.SupersessionFork{}, supersessions...), stale})
				}
				for _, value := range forks {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%d heads\tproposed %s\t%s\n", value.Ref(), len(value.Heads), value.Proposed().CheckpointID, singleLineText(value.Proposed().Task))
				}
				for _, value := range revisions {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%d heads\tproposed %s\t%s\n", value.Ref(), len(value.Heads), revisionHeadLabel(value.Proposed()), singleLineText(value.Proposed().Title))
				}
				for _, value := range supersessions {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%d heads\tproposed %s\t%s\n", value.Ref(), len(value.Heads), value.Proposed().ID, singleLineText(value.Title))
				}
				for _, value := range stale {
					writeStaleLine(cmd.OutOrStdout(), value)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Forks: %d. %s\n", len(forks)+len(revisions)+len(supersessions)+len(stale), forkDisclaimer)
				return nil
			})
		},
	}
	show := &cobra.Command{
		Use:   "show task:ID|TYPE:ID@REVISION|FAMILY:ID|stale:KIND:ID",
		Short: "Show a fork's heads, newest (proposed) first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			if staleSubject(args[0]) {
				kind, id, err := lease.ParseStaleSubject(args[0])
				if err != nil {
					return err
				}
				return withApp(cmd, func(application *app.App) error {
					writes, err := lease.StaleWrites(cmd.Context(), application.DB, application.ProjectID, lease.StaleSubject(kind, id))
					if err != nil {
						return err
					}
					if len(writes) == 0 {
						return fmt.Errorf("%s has no unresolved stale write", lease.StaleSubject(kind, id))
					}
					value := writes[0]
					if format == "json" {
						return writeJSON(cmd.OutOrStdout(), value)
					}
					out := cmd.OutOrStdout()
					stamp, newer := value.Stamp, value.Newer
					fmt.Fprintf(out, "Stale write: %s\nWritten %s by %s under token %d of %s (instance %s)\n", value.Ref(), stamp.RecordedAt.UTC().Format("2006-01-02 15:04:05Z"), stamp.Holder, stamp.Token, stamp.Scope(), valueOrUnknown(stamp.InstanceID))
					fmt.Fprintf(out, "Token %d had been granted %s to %s (instance %s)\n", newer.Token, newer.AcquiredAt.UTC().Format("2006-01-02 15:04:05Z"), newer.HolderLabel(), valueOrUnknown(newer.InstanceID))
					fmt.Fprintf(out, "Proposed: lease (the write yields to the newer lease); --accept write keeps it as standing.\n\n%s\n", forkDisclaimer)
					return nil
				})
			}
			if fork.SupersessionSubject(args[0]) {
				return withApp(cmd, func(application *app.App) error {
					forks, err := fork.SupersessionForks(cmd.Context(), application.DB, application.ProjectID, strings.TrimSpace(args[0]))
					if err != nil {
						return err
					}
					if len(forks) == 0 {
						return fmt.Errorf("%s has no unresolved fork", strings.TrimSpace(args[0]))
					}
					value := forks[0]
					if format == "json" {
						return writeJSON(cmd.OutOrStdout(), value)
					}
					out := cmd.OutOrStdout()
					fmt.Fprintf(out, "Fork: %s (%d heads)\nSuperseded record: %s\n", value.Ref(), len(value.Heads), singleLineText(value.Title))
					for index, head := range value.Heads {
						label := "head"
						if index == 0 {
							label = "proposed head"
						}
						fmt.Fprintf(out, "\n%s %s:%s (%s, %d supersessions)\n  Title: %s\n", label, value.RecordFamily, head.ID, head.RecordedAt.UTC().Format("2006-01-02 15:04:05Z"), head.Depth, singleLineText(head.Title))
					}
					fmt.Fprintf(out, "\n%s\n", forkDisclaimer)
					return nil
				})
			}
			if revisionForkRef(args[0]) {
				return withApp(cmd, func(application *app.App) error {
					forks, err := fork.RevisionForks(cmd.Context(), application.DB, application.ProjectID, strings.TrimSpace(args[0]))
					if err != nil {
						return err
					}
					if len(forks) == 0 {
						return noRevisionFork(cmd, application, strings.TrimSpace(args[0]))
					}
					value := forks[0]
					if format == "json" {
						return writeJSON(cmd.OutOrStdout(), value)
					}
					out := cmd.OutOrStdout()
					fmt.Fprintf(out, "Fork: %s (%d versions)\n", value.Ref(), len(value.Heads))
					for index, head := range value.Heads {
						label := "head"
						if index == 0 {
							label = "proposed head"
						}
						fmt.Fprintf(out, "\n%s %s (%s)\n  Content hash: %s\n  Envelope hash: %s\n  Title: %s\n", label, revisionHeadLabel(head), head.RecordedAt.UTC().Format("2006-01-02 15:04:05Z"), head.ContentSHA256, head.EnvelopeSHA256, singleLineText(head.Title))
						if head.Status != "" {
							fmt.Fprintf(out, "  Status: %s\n", head.Status)
						}
					}
					fmt.Fprintf(out, "\n%s\n", forkDisclaimer)
					return nil
				})
			}
			task, err := forkTask(args[0])
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				forks, err := fork.CheckpointForks(cmd.Context(), application.DB, application.ProjectID, task)
				if err != nil {
					return err
				}
				if len(forks) == 0 {
					return fmt.Errorf("task %q has no unresolved fork", task)
				}
				value := forks[0]
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), value)
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "Fork: %s (%d heads)\n", value.Ref(), len(value.Heads))
				if value.Base != "" {
					fmt.Fprintf(out, "Common base: %s\n", value.Base)
				}
				for index, head := range value.Heads {
					label := "head"
					if index == 0 {
						label = "proposed head"
					}
					fmt.Fprintf(out, "\n%s %s (%s, %s, %d continuations)\n  Summary: %s\n  Next: %s\n", label, head.CheckpointID, head.CreatedAt.UTC().Format("2006-01-02 15:04:05Z"), head.Status, head.Depth, singleLineText(head.Summary), singleLineText(head.NextAction))
				}
				fmt.Fprintf(out, "\n%s\n", forkDisclaimer)
				return nil
			})
		},
	}
	var accept, resolver, authority string
	resolve := &cobra.Command{
		Use:   "resolve task:ID|TYPE:ID@REVISION|FAMILY:ID|stale:KIND:ID",
		Short: "Continue a fork from one head, keeping the others in history",
		Long:  "For a task, write a continuation checkpoint from the accepted head; for a record revision, append the accepted version as the record's next revision (nothing is appended when the version held here is accepted and later revisions already continue it); for a decision (decision:ID) or work narrative (narrative:ID) superseded more than once, write only the resolution, after which the rejected successors read as superseded by the accepted one. For a stale write (stale:KIND:ID), write only a resolution saying the write yields to the newer lease (--accept lease, the default) or stands (--accept write). The accepted head is the proposed, newest one unless --accept names another. A resolution record naming the rejected heads is written with it; both sync, so every machine converges on the accepted head.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat(output)
			if err != nil {
				return err
			}
			if strings.TrimSpace(resolver) == "" {
				return errors.New("--resolver is required")
			}
			if subject := strings.TrimSpace(args[0]); staleSubject(subject) {
				return withApp(cmd, func(application *app.App) error {
					result, err := application.ResolveStaleWrite(cmd.Context(), subject, strings.TrimSpace(accept), strings.TrimSpace(resolver), authority)
					if err != nil {
						return err
					}
					if format == "json" {
						return writeJSON(cmd.OutOrStdout(), struct {
							StaleResolution lease.StaleResolution `json:"stale_resolution"`
						}{result})
					}
					outcome := "it yields to the newer lease"
					if result.Accepted == lease.AcceptWrite {
						outcome = "it stands"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Resolved %s: %s; the record stays in history (resolution %s).\n", result.Ref(), outcome, result.ID)
					return nil
				})
			}
			if subject := strings.TrimSpace(args[0]); fork.SupersessionSubject(subject) {
				return withApp(cmd, func(application *app.App) error {
					result, err := application.ResolveSupersessionFork(cmd.Context(), subject, strings.TrimSpace(accept), strings.TrimSpace(resolver), authority)
					if err != nil {
						return err
					}
					if format == "json" {
						return writeJSON(cmd.OutOrStdout(), struct {
							Resolution fork.Resolution `json:"resolution"`
						}{result})
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Resolved %s: accepted %s; %s now read as superseded by it and stay in history (resolution %s).\n", subject, result.AcceptedHead, strings.Join(result.RejectedHeads, ", "), result.ID)
					return nil
				})
			}
			if subject := strings.TrimSpace(args[0]); revisionForkRef(subject) {
				return withApp(cmd, func(application *app.App) error {
					result, err := application.ResolveRevisionFork(cmd.Context(), subject, strings.TrimSpace(accept), strings.TrimSpace(resolver), authority)
					if err != nil && strings.HasSuffix(err.Error(), "has no unresolved fork") {
						return noRevisionFork(cmd, application, subject)
					}
					if err != nil {
						return err
					}
					if format == "json" {
						return writeJSON(cmd.OutOrStdout(), result)
					}
					rejected := make([]string, len(result.Resolution.RejectedHeads))
					for index, sha := range result.Resolution.RejectedHeads {
						rejected[index] = shortHash(sha)
					}
					outcome := "later revisions here already continue it"
					if result.Revision > 0 {
						outcome = fmt.Sprintf("appended its content as revision %d", result.Revision)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Resolved %s: accepted %s and %s; kept %s in history (resolution %s).\n", subject, shortHash(result.Resolution.AcceptedHead), outcome, strings.Join(rejected, ", "), result.Resolution.ID)
					return nil
				})
			}
			task, err := forkTask(args[0])
			if err != nil {
				return err
			}
			return withApp(cmd, func(application *app.App) error {
				result, err := application.ResolveCheckpointFork(cmd.Context(), task, strings.TrimSpace(accept), strings.TrimSpace(resolver), authority)
				if err != nil {
					return err
				}
				if format == "json" {
					return writeJSON(cmd.OutOrStdout(), result)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Resolved task:%s: continued %s as %s; kept %s in history (resolution %s).\n", task, result.Resolution.AcceptedHead, result.Continuation.ID, strings.Join(result.Resolution.RejectedHeads, ", "), result.Resolution.ID)
				return nil
			})
		},
	}
	resolve.Flags().StringVar(&accept, "accept", "", "head to accept: a checkpoint ID, at least 7 characters of a revision's envelope hash, a superseding record's ID, or lease or write for a stale write (default: the proposed, newest head; lease for a stale write)")
	resolve.Flags().StringVar(&resolver, "resolver", "", "who resolved the fork (required)")
	resolve.Flags().StringVar(&authority, "authority", "user_reported", "user_reported or caller_asserted")
	for _, command := range []*cobra.Command{list, show, resolve} {
		command.Flags().StringVar(&output, "output", "text", "text or json")
		parent.AddCommand(command)
	}
	return parent
}
