package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/foldersync"
	"github.com/llewm/llewm/components/continuity/internal/portfolio"
	"github.com/spf13/cobra"
)

func portfolioCommand() *cobra.Command {
	parent := &cobra.Command{
		Use: "portfolio", Short: "Route independent project histories to named Git or folder destinations",
		Long: "Manage machine-local project routes from any folder. Pin existing project IDs and independent lineages to explicit Git or folder destinations. Configuration does not transfer records; sync is manual. All readers of a destination container or repository can read every lineage stored there. Relay consent forwards the selected project's retained history, not a private subset.",
	}
	load := func() (portfolio.Store, portfolio.Catalog, error) {
		store, err := portfolio.DefaultStore()
		if err != nil {
			return store, portfolio.Catalog{}, err
		}
		catalog, err := store.Load()
		return store, catalog, err
	}
	show := &cobra.Command{Use: "show", Short: "Read local destinations and pinned project routes; no network or project writes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		_, catalog, err := load()
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), catalog)
	}}
	destinations := &cobra.Command{Use: "destination", Short: "Configure named Git or folder destinations"}
	var remote, clone, sshKey, kind, directory, storeID string
	var create bool
	addDestination := &cobra.Command{Use: "add NAME", Short: "Name an explicit Git or folder destination", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, _, err := load()
		if err != nil {
			return err
		}
		catalog, err := store.Update(func(c *portfolio.Catalog) error {
			d := portfolio.Destination{Name: args[0], Kind: kind, Remote: remote, Clone: clone, SSHKey: sshKey, Directory: directory, StoreID: storeID}
			if kind != "folder" && (create || storeID != "" || directory != "") {
				return fmt.Errorf("--create, --directory and --store-id require --kind folder")
			}
			if kind == "folder" {
				if create && storeID != "" {
					return fmt.Errorf("--create does not accept --store-id")
				}
				if directory == "" {
					return fmt.Errorf("--directory is required for folder destinations")
				}
				d.Directory, err = filepath.Abs(directory)
				if err != nil {
					return err
				}
				if create {
					d.StoreID = strings.Repeat("0", 32)
				} else if _, err := foldersync.Pin(d.Directory, storeID, false); err != nil {
					return err
				}
			}
			c.Destinations = append(c.Destinations, d)
			if err := c.Validate(); err != nil {
				return err
			}
			if kind == "folder" {
				// Validate all known project storage before marker creation. Configuration
				// never changes a provider's sharing policy or initializes a project.
				if err := foldersync.Separate(d.Directory, store.Directory); err != nil {
					return err
				}
				for _, p := range c.Projects {
					cfg, err := config.Load(p.Root)
					if err != nil {
						return err
					}
					if err := portfolio.ValidateStorage(*c, d, cfg, p); err != nil {
						return err
					}
				}
				marker, err := foldersync.Pin(d.Directory, storeID, create)
				if err != nil {
					return err
				}
				c.Destinations[len(c.Destinations)-1].StoreID = marker.StoreID
			}
			return nil
		})
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), catalog)
	}}
	addDestination.Flags().StringVar(&remote, "remote", "", "exact SSH, HTTP(S), or absolute local Git repository destination for --kind git")
	addDestination.Flags().StringVar(&clone, "clone", "", "absolute existing owned memory clone or separate new clone path; default shared per remote")
	addDestination.Flags().StringVar(&sshKey, "ssh-key", "", "absolute private-key path used only for this destination; no key contents are stored")
	addDestination.Flags().StringVar(&kind, "kind", "", "git or folder (required)")
	addDestination.Flags().StringVar(&directory, "directory", "", "existing real folder container directory for --kind folder")
	addDestination.Flags().StringVar(&storeID, "store-id", "", "exact inspected folder store ID when joining")
	addDestination.Flags().BoolVar(&create, "create", false, "explicitly create a folder container marker")
	_ = addDestination.MarkFlagRequired("kind")
	removeDestination := &cobra.Command{Use: "remove NAME", Short: "Remove an unused destination; preserve its shared files, clone and remote history", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, _, err := load()
		if err != nil {
			return err
		}
		catalog, err := store.Update(func(c *portfolio.Catalog) error {
			for _, p := range c.Projects {
				for _, r := range p.Routes {
					if r.Destination == args[0] {
						return fmt.Errorf("destination is still routed by project %s; remove that project binding first", p.Name)
					}
				}
			}
			for i, d := range c.Destinations {
				if d.Name == args[0] {
					c.Destinations = append(c.Destinations[:i], c.Destinations[i+1:]...)
					return nil
				}
			}
			return fmt.Errorf("portfolio destination not found")
		})
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), catalog)
	}}
	destinations.AddCommand(addDestination, removeDestination)
	projects := &cobra.Command{Use: "project", Short: "Inspect and bind existing independent projects"}
	inspect := &cobra.Command{Use: "inspect PATH", Short: "Read the owning root, project ID and existing lineage without initializing", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		p, err := portfolio.Inspect(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), p)
	}}
	var root, projectID, lineage string
	var names []string
	addProject := &cobra.Command{Use: "add NAME", Short: "Pin a project to explicit destinations; relay starts disabled", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		p, err := portfolio.Inspect(cmd.Context(), root)
		if err != nil {
			return err
		}
		if p.ProjectID != projectID || p.Lineage != lineage {
			return fmt.Errorf("--confirm-project and --confirm-lineage must match the inspected source")
		}
		p.Name = args[0]
		for _, name := range names {
			p.Routes = append(p.Routes, portfolio.Route{Destination: name})
		}
		store, _, err := load()
		if err != nil {
			return err
		}
		catalog, err := store.Update(func(c *portfolio.Catalog) error {
			c.Projects = append(c.Projects, p)
			return nil
		})
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), catalog)
	}}
	addProject.Flags().StringVar(&root, "root", "", "existing source project or member path (required)")
	addProject.Flags().StringVar(&projectID, "confirm-project", "", "exact inspected local project ID (required)")
	addProject.Flags().StringVar(&lineage, "confirm-lineage", "", "exact inspected portable lineage (required)")
	addProject.Flags().StringSliceVar(&names, "destination", nil, "named destination(s); repeat or comma-separate (required)")
	for _, name := range []string{"root", "confirm-project", "confirm-lineage", "destination"} {
		_ = addProject.MarkFlagRequired(name)
	}
	removeProject := &cobra.Command{Use: "remove NAME", Short: "Remove a local routing binding; preserve project and all published history", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, _, err := load()
		if err != nil {
			return err
		}
		catalog, err := store.Update(func(c *portfolio.Catalog) error {
			for i, p := range c.Projects {
				if p.Name == args[0] {
					c.Projects = append(c.Projects[:i], c.Projects[i+1:]...)
					return nil
				}
			}
			return fmt.Errorf("portfolio project not found")
		})
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), catalog)
	}}
	var enabled bool
	var relayLineage string
	relay := &cobra.Command{Use: "relay PROJECT DESTINATION", Short: "Authorize forwarding this project's received history to one destination", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("enable") {
			return fmt.Errorf("--enable=true/false is required")
		}
		store, _, err := load()
		if err != nil {
			return err
		}
		catalog, err := store.Update(func(c *portfolio.Catalog) error {
			for i, p := range c.Projects {
				if p.Name != args[0] {
					continue
				}
				if p.Lineage != relayLineage {
					return fmt.Errorf("confirmed lineage differs from pinned project")
				}
				for j, r := range p.Routes {
					if r.Destination == args[1] {
						c.Projects[i].Routes[j].Relay = enabled
						return nil
					}
				}
			}
			return fmt.Errorf("portfolio project route not found")
		})
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), catalog)
	}}
	relay.Flags().BoolVar(&enabled, "enable", false, "explicit true/false for this project/destination")
	relay.Flags().StringVar(&relayLineage, "confirm-lineage", "", "exact pinned project lineage (required)")
	_ = relay.MarkFlagRequired("confirm-lineage")
	projects.AddCommand(inspect, addProject, removeProject)
	var all, dryRun bool
	var output string
	sync := &cobra.Command{Use: "sync [PROJECT]", Short: "Explicitly sync one project or --all; --dry-run reads pins and previews destinations", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		format, err := outputFormat(output)
		if err != nil {
			return err
		}
		_, catalog, err := load()
		if err != nil {
			return err
		}
		name := ""
		if len(args) != 0 {
			name = args[0]
		}
		results, runErr := portfolio.Run(cmd.Context(), catalog, name, all, dryRun)
		if format == "json" {
			if err := writeJSON(cmd.OutOrStdout(), results); err != nil {
				return err
			}
		} else {
			for _, result := range results {
				fmt.Fprintf(cmd.OutOrStdout(), "Project %s (%s): %s\n", result.Name, result.Lineage, result.Outcome)
				if result.Error != "" {
					fmt.Fprintln(cmd.OutOrStdout(), result.Error)
				}
				for _, d := range result.Destinations {
					location := d.Remote
					if d.Kind == "folder" {
						location = d.Directory
					}
					fmt.Fprintf(cmd.OutOrStdout(), "  %s (%s): %s; relay=%t; %s\n", d.Name, d.Kind, d.Outcome, d.Relay, location)
				}
			}
		}
		return runErr
	}}
	sync.Flags().BoolVar(&all, "all", false, "explicitly select every pinned project")
	sync.Flags().BoolVar(&dryRun, "dry-run", false, "validate local project pins and preview routes; no archive, clone, network or writable database")
	sync.Flags().StringVar(&output, "output", "text", "text or json")
	var statusAll bool
	status := &cobra.Command{Use: "status [PROJECT]", Short: "Read per-destination sync attempts without contacting a remote", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, catalog, err := load()
		if err != nil {
			return err
		}
		name := ""
		if len(args) != 0 {
			name = args[0]
		}
		results, readErr := portfolio.ReadStatus(cmd.Context(), catalog, name, statusAll)
		if err := writeJSON(cmd.OutOrStdout(), results); err != nil {
			return err
		}
		return readErr
	}}
	status.Flags().BoolVar(&statusAll, "all", false, "explicitly select every pinned project")
	parent.AddCommand(show, destinations, projects, relay, sync, status)
	return parent
}
