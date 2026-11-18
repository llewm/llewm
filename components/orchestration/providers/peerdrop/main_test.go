package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/orchestration/internal/connectionprofile"
)

func TestICEURLs(t *testing.T) {
	for _, raw := range []string{`"stun:one"`, `["stun:one"]`} {
		var urls iceURLs
		if err := json.Unmarshal([]byte(raw), &urls); err != nil || len(urls) != 1 || urls[0] != "stun:one" {
			t.Fatalf("unexpected ICE list: %v %v", urls, err)
		}
	}
}

func TestReceivedFilesAreExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "red.json")
	h := handoff{Profile: connectionprofile.Profile{Version: 1, Endpoint: "https://example.com", Project: "red", Authority: strings.Repeat("a", 48)}, Credential: "test fixture"}
	if err := savePair(path, h); err != nil {
		t.Fatal(err)
	}
	if err := savePair(path, h); err == nil {
		t.Fatal("overwrote credential")
	}
	if err := os.Remove(path + ".token"); err != nil {
		t.Fatal(err)
	}
	if err := savePair(path, h); err == nil {
		t.Fatal("overwrote profile")
	}
	if _, err := os.Stat(path + ".token"); !os.IsNotExist(err) {
		t.Fatal("left partial credential")
	}
}

func TestComparisonBindsFreshConnection(t *testing.T) {
	toGuest := make(chan []byte, 2)
	toHost := make(chan []byte, 1)
	var transcript [][]byte
	hostCode := make(chan string, 1)
	go func() {
		code, err := exchangeNonces(true, "first-channel", func(b []byte) error { toGuest <- b; return nil }, func() ([]byte, error) { return <-toHost, nil })
		if err != nil {
			code = ""
		}
		hostCode <- code
	}()
	code, err := exchangeNonces(false, "first-channel", func(b []byte) error { toHost <- b; return nil }, func() ([]byte, error) { frame := <-toGuest; transcript = append(transcript, frame); return frame, nil })
	if err != nil || len(code) != 9 || code != <-hostCode {
		t.Fatal("comparison failed", err)
	}
	// A fresh DTLS connection has new certificates. Captured commitment and
	// reveal must fail before either side can approve a credential transfer.
	_, err = exchangeNonces(false, "new-channel", func([]byte) error { return nil }, func() ([]byte, error) { frame := transcript[0]; transcript = transcript[1:]; return frame, nil })
	if err == nil {
		t.Fatal("accepted a commitment from another connection")
	}
}
