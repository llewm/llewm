package main

import (
	"fmt"
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/spf13/cobra"
)

func logCommand() *cobra.Command {
	var count int
	var repo string
	cmd := &cobra.Command{Use: "log [revision]", Short: "Show Git commits with their Llewm narratives", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ref := "HEAD"
		if len(args) > 0 {
			ref = args[0]
		}
		return withApp(cmd, func(application *app.App) error {
			output, err := application.Narratives.Log(cmd.Context(), application.ProjectID, repo, ref, count)
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), output)
			return err
		})
	}}
	cmd.Flags().IntVarP(&count, "max-count", "n", 20, "maximum commits (1-1000)")
	cmd.Flags().StringVar(&repo, "repo", "", "workspace member repository")
	return cmd
}
