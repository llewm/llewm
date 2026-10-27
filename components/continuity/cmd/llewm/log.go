package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// pageLog invokes only the standard less executable, never a shell or a command
// supplied by untrusted project data. Redirected output remains plain text.
func pageLog(out io.Writer, text string, disabled bool) error {
	f, ok := out.(*os.File)
	if disabled || !ok || !term.IsTerminal(int(f.Fd())) || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		_, err := io.WriteString(out, text)
		return err
	}
	path, err := exec.LookPath("less")
	if err != nil {
		_, err = io.WriteString(out, text)
		return err
	}
	pager := exec.Command(path, "-FRX")
	pager.Stdin = strings.NewReader(text)
	pager.Stdout = f
	pager.Stderr = os.Stderr
	return pager.Run()
}

func logCommand() *cobra.Command {
	var options worknarrative.LogOptions
	var noPager bool
	cmd := &cobra.Command{Use: "log [revision-or-range]", Short: "Show Git commits with their Llewm narratives", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			options.Revision = args[0]
		}
		if options.Expand && !options.Oneline {
			return fmt.Errorf("--expand requires --oneline")
		}
		return withApp(cmd, func(application *app.App) error {
			output, err := application.Narratives.Log(cmd.Context(), application.ProjectID, options)
			if err != nil {
				return err
			}
			return pageLog(cmd.OutOrStdout(), output, noPager)
		})
	}}
	cmd.Flags().IntVarP(&options.Limit, "max-count", "n", 20, "maximum displayed commits (1-1000)")
	cmd.Flags().StringVar(&options.Member, "repo", "", "workspace member repository")
	cmd.Flags().StringVar(&options.Author, "author", "", "Git author pattern")
	cmd.Flags().StringVar(&options.Since, "since", "", "commits after this Git date")
	cmd.Flags().StringVar(&options.Until, "until", "", "commits before this Git date")
	cmd.Flags().StringArrayVar(&options.Paths, "path", nil, "Git pathspec filter (repeatable)")
	cmd.Flags().BoolVar(&options.Oneline, "oneline", false, "show one line per commit")
	cmd.Flags().BoolVar(&options.Expand, "expand", false, "include narratives with --oneline")
	cmd.Flags().BoolVar(&options.Missing, "missing", false, "show commits without a current narrative")
	cmd.Flags().BoolVar(&noPager, "no-pager", false, "write directly without a terminal pager")
	return cmd
}
