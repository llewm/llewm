// Command notecheck compares this project's Git notes with directly recorded
// work narratives during the transition away from notes. It is a development
// aid, not part of the continuity CLI:
//
//	go run ./internal/noteimport/notecheck -since 9b61ad7
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/gitenv"
	"github.com/llewm/llewm/components/continuity/internal/noteimport"
)

func main() {
	since := flag.String("since", "", "exclusive start commit; commits after it up to HEAD are checked (required)")
	asJSON := flag.Bool("json", false, "emit JSON")
	flag.Parse()
	if err := run(*since, *asJSON); err != nil {
		fmt.Fprintln(os.Stderr, "notecheck:", err)
		os.Exit(1)
	}
}

func run(since string, asJSON bool) error {
	if since == "" {
		return fmt.Errorf("-since is required")
	}
	ctx := context.Background()
	top, err := gitenv.Command(ctx, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return fmt.Errorf("find repository root: %w", err)
	}
	root := strings.TrimSpace(string(top))
	db, err := database.OpenReadOnly(ctx, filepath.Join(root, ".llewm/continuity", "continuity.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	var projectID string
	if err = db.QueryRowContext(ctx, `SELECT id FROM projects WHERE root=?`, root).Scan(&projectID); err != nil {
		return fmt.Errorf("find project for %s: %w", root, err)
	}
	report, err := noteimport.Service{DB: db, Root: root}.Check(ctx, projectID, since)
	if err != nil {
		return err
	}
	if asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	fmt.Print(report.Text())
	return nil
}
