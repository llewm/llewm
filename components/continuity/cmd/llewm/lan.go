package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/lantransfer"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/spf13/cobra"
)

func readLANBundle(directory string) (portable.Bundle, error) {
	manifest, records, err := portable.ReadBundleDirectory(directory)
	if err != nil {
		return portable.Bundle{}, err
	}
	return portable.ValidateBundle(manifest, records)
}

func memoryLANCommand() *cobra.Command {
	parent := &cobra.Command{Use: "lan", Short: "Temporarily exchange curated bundles with an explicitly paired LAN peer", Long: `Exchange already exported portable bundles over a temporary authenticated TLS connection.
No project database, Git repository, sync configuration, or imported record is changed.
Inspect outgoing data before sharing. Deliver the private pairing file through a trusted
channel; it contains a short-lived bearer credential and must never enter agent history.
After transfer, inspect and plan the received bundle before explicitly applying it.`}
	var serveDirectory, serveReceive, listen, servePairing string
	var lifetime time.Duration
	serve := &cobra.Command{Use: "serve", Short: "Offer one bundle exchange on a specific LAN address", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if serveDirectory == "" || serveReceive == "" || listen == "" || servePairing == "" {
			return fmt.Errorf("--directory, --receive-directory, --listen and --pairing-file are required")
		}
		outgoing, err := readLANBundle(serveDirectory)
		if err != nil {
			return err
		}
		server, err := lantransfer.NewServer(listen, outgoing, serveReceive, lifetime)
		if err != nil {
			return err
		}
		defer server.Close()
		if err := lantransfer.WritePairing(servePairing, server.Pairing()); err != nil {
			return err
		}
		defer os.Remove(servePairing)
		fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for one paired exchange on %s until %s. Deliver the private pairing file to the peer through a trusted channel.\n", server.Pairing().Address, server.Pairing().ExpiresAt.UTC().Format(time.RFC3339))
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		result, err := server.Serve(ctx)
		if err != nil {
			return fmt.Errorf("LAN exchange ended: %w; a received bundle may remain at the selected destination; inspect before retrying", err)
		}
		return writeJSON(cmd.OutOrStdout(), result)
	}}
	serve.Flags().StringVar(&serveDirectory, "directory", "", "inspected outgoing portable bundle (required)")
	serve.Flags().StringVar(&serveReceive, "receive-directory", "", "new private incoming bundle directory (required, unused for a receive-only peer)")
	serve.Flags().StringVar(&listen, "listen", "", "specific private IP:port to bind; port 0 selects a free port (required)")
	serve.Flags().StringVar(&servePairing, "pairing-file", "", "new private credential file, removed when listener exits (required)")
	serve.Flags().DurationVar(&lifetime, "lifetime", 5*time.Minute, "pairing lifetime, at most 10m")
	var exchangeDirectory, exchangeReceive, exchangePairing string
	exchange := &cobra.Command{Use: "exchange", Short: "Receive history and optionally send an inspected bundle to a paired peer", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if exchangeReceive == "" || exchangePairing == "" {
			return fmt.Errorf("--receive-directory and --pairing-file are required")
		}
		pairing, err := lantransfer.ReadPairing(exchangePairing)
		if err != nil {
			return err
		}
		var outgoing *portable.Bundle
		if exchangeDirectory != "" {
			bundle, err := readLANBundle(exchangeDirectory)
			if err != nil {
				return err
			}
			outgoing = &bundle
		}
		result, err := lantransfer.Exchange(cmd.Context(), pairing, outgoing, exchangeReceive)
		if err != nil {
			return fmt.Errorf("LAN exchange ended: %w; inspect both receive destinations before retrying", err)
		}
		return writeJSON(cmd.OutOrStdout(), result)
	}}
	exchange.Flags().StringVar(&exchangeDirectory, "directory", "", "inspected outgoing bundle; omit for a fresh receive-only install")
	exchange.Flags().StringVar(&exchangeReceive, "receive-directory", "", "new private incoming bundle directory (required)")
	exchange.Flags().StringVar(&exchangePairing, "pairing-file", "", "privately received credential file (required; delete after exchange)")
	parent.AddCommand(serve, exchange)
	return parent
}
