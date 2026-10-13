package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/llewm/llewm/components/reporting/internal/intake"
)

var version = "0.1.0-dev"
var commit = "unknown"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "llewm-report:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Fprintln(os.Stdout, "Usage: llewm-report queue|deliver|status --config FILE [--file REPORT.json]\n       llewm-report version\n\nQueue selected reports locally, deliver to an opted-in private Forgejo tracker, or inspect receipts.")
		return nil
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: llewm-report queue|deliver|status --config FILE [--file REPORT.json], or llewm-report version")
	}
	if args[0] == "version" && len(args) == 1 {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"version": version, "commit": commit, "report_schema_version": 1, "status_schema_version": 1})
	}
	if args[0] != "queue" && args[0] != "deliver" && args[0] != "status" {
		return fmt.Errorf("unknown command")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	var configPath, file, projectID string
	flags.StringVar(&configPath, "config", "", "explicit reporting configuration file (required)")
	flags.StringVar(&projectID, "project-id", "", "status: exact source project filter")
	flags.StringVar(&file, "file", "", "queue: selected report JSON file")
	if err := flags.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || configPath == "" {
		return fmt.Errorf("--config is required; positional arguments are not accepted")
	}
	if (args[0] == "queue") != (file != "") {
		return fmt.Errorf("--file is required only for queue")
	}
	if projectID != "" && args[0] != "status" {
		return fmt.Errorf("--project-id applies only to status")
	}
	config, err := intake.LoadConfig(configPath)
	if err != nil {
		return err
	}
	adapter := intake.Adapter{Config: config}
	var result any
	switch args[0] {
	case "queue":
		entry, queueErr := adapter.Queue(file)
		err = queueErr
		if entry != nil {
			result = map[string]any{"id": entry.ID, "status": entry.Status, "target": entry.Target}
		}
	case "status":
		result, err = adapter.Snapshot(projectID)
	case "deliver":
		result, err = adapter.Deliver(context.Background())
	}
	// Partial receipts remain visible even when a later report fails.
	if result != nil {
		if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
			return encodeErr
		}
	}
	return err
}
