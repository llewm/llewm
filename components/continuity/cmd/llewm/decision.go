package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/decision"
)

func decisionCommand() *cobra.Command {
	parent := &cobra.Command{Use: "decision", Short: "Record reviewed decisions; read them with history search and history show"}
	var file string
	record := &cobra.Command{Use: "record", Short: "Append one reviewed decision from a bounded JSON file", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if file == "" {
			return errors.New("--file is required")
		}
		body, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if len(body) > 32<<10 {
			return errors.New("decision file exceeds 32 KiB")
		}
		var value decision.Record
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&value); err != nil {
			return err
		}
		var extra any
		if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return errors.New("decision file must contain one JSON object")
		}
		return withApp(cmd, func(application *app.App) error {
			value.ProjectID = application.ProjectID
			created, err := application.Decisions.Append(cmd.Context(), value)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), created)
		})
	}}
	record.Flags().StringVar(&file, "file", "", "JSON decision record file")

	parent.AddCommand(record)
	return parent
}
