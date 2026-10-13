package main

import (
	"errors"
	"io"
	"os"

	"github.com/llewm/llewm/components/continuity/internal/pmadapter"
	"github.com/spf13/cobra"
)

func pmCommand() *cobra.Command {
	parent := &cobra.Command{Use: "pm", Short: "Inspect untrusted project-management interchange without persistence"}
	var path string
	var expected pmadapter.Project
	inspect := &cobra.Command{Use: "inspect", Short: "Validate one local PM packet and emit sanitized JSON (no database access)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		data, err := readPMFile(path)
		if err != nil {
			return err
		}
		report, err := pmadapter.Inspect(data, expected)
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), report)
	}}
	inspect.Flags().StringVar(&path, "file", "", "explicit regular JSON file, at most 1 MiB")
	inspect.Flags().StringVar(&expected.Provider, "provider", "", "expected provider identity")
	inspect.Flags().StringVar(&expected.Instance, "instance", "", "expected provider instance/tenant identity")
	inspect.Flags().StringVar(&expected.Project, "project", "", "expected external project identity (not project-root)")
	inspect.PreRunE = func(_ *cobra.Command, _ []string) error {
		if path == "" || expected.Provider == "" || expected.Instance == "" || expected.Project == "" {
			return errors.New("pm inspect requires --file, --provider, --instance and --project")
		}
		return nil
	}
	parent.AddCommand(inspect)
	return parent
}

func readPMFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > pmadapter.MaxBytes {
		return nil, errors.New("pm file must be an accessible regular file of at most 1 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("pm file could not be opened")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return nil, errors.New("pm file identity changed")
	}
	data, err := io.ReadAll(io.LimitReader(f, pmadapter.MaxBytes+1))
	if err != nil {
		return nil, errors.New("pm file could not be read")
	}
	if len(data) > pmadapter.MaxBytes {
		return nil, errors.New("pm file exceeds 1 MiB")
	}
	return data, nil
}
