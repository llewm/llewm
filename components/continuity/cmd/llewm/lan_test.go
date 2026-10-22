package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/lantransfer"
	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestLANFreshInstallJoinsAndRecoversExactCheckpoint(t *testing.T) {
	source := testProject(t)
	runCLI(t, "", "init")
	var original portable.Identity
	decodeCLI(t, runCLI(t, "", "sync", "init"), &original)
	checkpoint := runCLI(t, "", "checkpoint", "create", "--task-id", "lan-recovery", "--task", "Continue imported work", "--summary", "Preserve exact recovery", "--next", "Verify receiver")
	var saved struct {
		ID string `json:"id"`
	}
	saved.ID = strings.TrimSpace(checkpoint)
	if saved.ID == "" {
		t.Fatal("no source checkpoint")
	}
	exported := filepath.Join(source, "export")
	runCLI(t, "", "memory", "export", "--directory", exported)
	outgoing, err := readLANBundle(exported)
	if err != nil {
		t.Fatal(err)
	}
	hostIncoming := filepath.Join(source, "incoming")
	server, err := lantransfer.NewServer("127.0.0.1:0", outgoing, hostIncoming, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := server.Serve(ctx); done <- err }()
	pairing := filepath.Join(t.TempDir(), "pairing")
	if err := lantransfer.WritePairing(pairing, server.Pairing()); err != nil {
		t.Fatal(err)
	}

	// Receiving must work before init, outside any configured lineage or sync repo.
	target := testProject(t)
	incoming := filepath.Join(target, "received")
	var transferred lantransfer.Result
	decodeCLI(t, runCLI(t, "", "memory", "lan", "exchange", "--pairing-file", pairing, "--receive-directory", incoming), &transferred)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !transferred.Received || transferred.Sent || !transferred.ImportRequired {
		t.Fatalf("wrong transfer receipt: %+v", transferred)
	}
	if _, err := os.Stat(filepath.Join(target, ".llewm")); !os.IsNotExist(err) {
		t.Fatalf("transfer created project state: %v", err)
	}
	runCLI(t, "", "memory", "inspect", "--directory", incoming)
	runCLI(t, "", "init")
	if got := runCLI(t, "", "sync", "status"); !strings.Contains(got, "Identity: none") {
		t.Fatalf("init invented lineage: %s", got)
	}
	var identity portable.Identity
	decodeCLI(t, runCLI(t, "", "sync", "init", "--lineage", original.LineageID), &identity)
	if identity.LineageID != original.LineageID || identity.InstanceID == original.InstanceID {
		t.Fatalf("wrong receiving identity: %+v", identity)
	}
	var plan portable.ImportPlan
	decodeCLI(t, runCLI(t, "", "memory", "plan", "--directory", incoming), &plan)
	if !plan.ApplyAllowed {
		t.Fatalf("fresh recovery refused: %+v", plan)
	}
	args := []string{"memory", "apply", "--directory", incoming, "--confirm-lineage", identity.LineageID, "--confirm-project", identity.ProjectID}
	var applied portable.ImportApplyResult
	decodeCLI(t, runCLI(t, "", args...), &applied)
	if applied.Receipt == nil || applied.Receipt.AppendedRecords == 0 {
		t.Fatalf("no imported records: %+v", applied)
	}
	got := runCLI(t, "", "bootstrap", "--checkpoint-id", saved.ID)
	if !strings.Contains(got, saved.ID) || !strings.Contains(got, "Preserve exact recovery") {
		t.Fatalf("exact checkpoint not recovered: %s", got)
	}
	decodeCLI(t, runCLI(t, "", args...), &applied)
	if applied.Receipt.AppendedRecords != 0 {
		t.Fatal("replay appended duplicate history")
	}
	config, err := os.ReadFile(filepath.Join(target, ".llewm", "continuity", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "[sync]") {
		t.Fatalf("receiver configured sync: %s", config)
	}
	if err, _ := runCLIError("sync", "init", "--lineage", "lineage:"+strings.Repeat("b", 32)); err == nil {
		t.Fatal("silently changed lineage")
	}
}

func TestLANCommandsDoNotInitializeStateOnInvalidInput(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	state := filepath.Join(root, "state")
	t.Setenv("CONTINUITY_HOME", state)
	t.Setenv("CONTINUITY_GLOBAL_HOME", filepath.Join(root, "global"))
	for _, args := range [][]string{{"memory", "lan", "serve"}, {"memory", "lan", "exchange"}, {"memory", "lan", "exchange", "--pairing-file", filepath.Join(root, "missing"), "--receive-directory", filepath.Join(root, "incoming")}} {
		if err, _ := runCLIError(args...); err == nil {
			t.Fatalf("accepted invalid input: %v", args)
		}
	}
	for _, path := range []string{state, filepath.Join(root, "global"), filepath.Join(root, "incoming")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("invalid LAN command created %s: %v", path, err)
		}
	}
}

type lanReadyWriter struct{ ready chan struct{} }

func (w lanReadyWriter) Write(p []byte) (int, error) { close(w.ready); return len(p), nil }

func TestLANServeCommandRemovesPairingAndPublishesNoProjectState(t *testing.T) {
	// Exercise the CLI listener's credential lifecycle as well as the library.
	root := testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "sync", "init")
	outgoing := filepath.Join(root, "outgoing")
	runCLI(t, "", "memory", "export", "--directory", outgoing)
	clean := t.TempDir()
	t.Chdir(clean)
	state := filepath.Join(clean, "state")
	t.Setenv("CONTINUITY_HOME", state)
	t.Setenv("CONTINUITY_GLOBAL_HOME", filepath.Join(clean, "global"))
	pairingPath := filepath.Join(clean, "pairing")
	incoming := filepath.Join(clean, "incoming")
	cmd := newRootCommand("llewm")
	cmd.SetArgs([]string{"memory", "lan", "serve", "--directory", outgoing, "--receive-directory", incoming, "--listen", "127.0.0.1:0", "--pairing-file", pairingPath})
	ready := make(chan struct{})
	cmd.SetErr(lanReadyWriter{ready})
	var output bytes.Buffer
	cmd.SetOut(&output)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd.SetContext(ctx)
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("listener never became ready")
	}
	pairing, err := lantransfer.ReadPairing(pairingPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lantransfer.Exchange(ctx, pairing, nil, filepath.Join(clean, "received")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{pairingPath, state, filepath.Join(clean, "global"), incoming} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected retained state %s: %v", path, err)
		}
	}
	if strings.Contains(output.String(), pairing.Secret) {
		t.Fatal("credential appeared in command receipt")
	}
}
