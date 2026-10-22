package lantransfer

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func bundle(t *testing.T, lineage, id string) portable.Bundle {
	t.Helper()
	value, err := portable.BuildBundle(portable.BundleSpec{ExportID: "export:" + id, LineageID: lineage, SourceInstanceID: "instance:" + strings.Repeat("2", 32), CreatedAt: time.Now(), SourceDatabaseSchema: "079", SelectionProfile: "curated-v1", RedactionProfile: portable.DefaultRedactionProfile(), Omissions: []portable.Omission{}}, []portable.EnvelopeSpec{{LineageID: lineage, RecordType: "work_narrative", RecordSchemaVersion: 1, RecordID: id, RecordedAt: time.Now(), Authority: "caller_asserted", Index: json.RawMessage(`{"title":"Exchange fixture"}`), Payload: json.RawMessage(`{"body":"curated fixture"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func start(t *testing.T, outgoing portable.Bundle) (*Server, string, <-chan error) {
	t.Helper()
	incoming := filepath.Join(t.TempDir(), "received")
	server, err := NewServer("127.0.0.1:0", outgoing, incoming, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = server.Close() })
	done := make(chan error, 1)
	go func() { _, err := server.Serve(ctx); done <- err }()
	return server, incoming, done
}

func TestExchangeBidirectionalAndReceiveOnly(t *testing.T) {
	lineage := "lineage:" + strings.Repeat("a", 32)
	for _, receiveOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "bidirectional", true: "fresh_receiver"}[receiveOnly], func(t *testing.T) {
			a, b := bundle(t, lineage, "a"), bundle(t, lineage, "b")
			server, hostIncoming, done := start(t, a)
			var outgoing *portable.Bundle
			if !receiveOnly {
				outgoing = &b
			}
			target := filepath.Join(t.TempDir(), "received")
			result, err := Exchange(context.Background(), server.Pairing(), outgoing, target)
			if err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if !result.ImportRequired || !result.Received || result.Sent == receiveOnly || result.LineageID != lineage {
				t.Fatalf("bad receipt: %+v", result)
			}
			incoming, err := portable.InspectBundleDirectory(target)
			if err != nil || incoming.Manifest.RecordSetSHA256 != a.Manifest.RecordSetSHA256 {
				t.Fatalf("wrong received bundle: %+v %v", incoming, err)
			}
			if receiveOnly {
				if _, err := os.Stat(hostIncoming); !os.IsNotExist(err) {
					t.Fatalf("receive-only peer published an upload: %v", err)
				}
			} else {
				incoming, err = portable.InspectBundleDirectory(hostIncoming)
				if err != nil || incoming.Manifest.RecordSetSHA256 != b.Manifest.RecordSetSHA256 {
					t.Fatalf("wrong upload: %+v %v", incoming, err)
				}
			}
			if _, err := Exchange(context.Background(), server.Pairing(), nil, filepath.Join(t.TempDir(), "replay")); err == nil {
				t.Fatal("pairing reused")
			}
		})
	}
}

func TestUntrustedPairingDoesNotRevealOrPublishHistory(t *testing.T) {
	a := bundle(t, "lineage:"+strings.Repeat("a", 32), "a")
	server, hostIncoming, done := start(t, a)
	for _, change := range []string{"pin", "secret", "expiry"} {
		p := server.Pairing()
		switch change {
		case "pin":
			p.CertificateSHA256 = strings.Repeat("0", 64)
		case "secret":
			p.Secret = strings.Repeat("0", 64)
		case "expiry":
			p.ExpiresAt = time.Now().Add(-time.Second)
		}
		target := filepath.Join(t.TempDir(), "received")
		if _, err := Exchange(context.Background(), p, nil, target); err == nil {
			t.Fatalf("accepted %s", change)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("published after %s: %v", change, err)
		}
	}
	if _, err := os.Stat(hostIncoming); !os.IsNotExist(err) {
		t.Fatalf("published unauthenticated upload: %v", err)
	}
	// Invalid credentials do not consume the valid pairing or change its pin.
	if _, err := Exchange(context.Background(), server.Pairing(), nil, filepath.Join(t.TempDir(), "received")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDifferentLineageConsumesPairingWithoutPublication(t *testing.T) {
	a := bundle(t, "lineage:"+strings.Repeat("a", 32), "a")
	b := bundle(t, "lineage:"+strings.Repeat("b", 32), "b")
	server, hostIncoming, done := start(t, a)
	target := filepath.Join(t.TempDir(), "received")
	if _, err := Exchange(context.Background(), server.Pairing(), &b, target); err == nil {
		t.Fatal("different lineages accepted")
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "lineages") {
		t.Fatalf("server did not reject lineage: %v", err)
	}
	for _, path := range []string{target, hostIncoming} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("published mismatched lineage: %v", err)
		}
	}
}

func TestFrameBoundsTamperingAndTruncation(t *testing.T) {
	a := bundle(t, "lineage:"+strings.Repeat("a", 32), "a")
	var valid bytes.Buffer
	if err := writeBundle(&valid, &a); err != nil {
		t.Fatal(err)
	}
	var oversized [8]byte
	binary.BigEndian.PutUint32(oversized[:4], portable.MaxManifestBytes+1)
	cases := [][]byte{nil, make([]byte, 8), oversized[:], valid.Bytes()[:valid.Len()-1]}
	tampered := bytes.Clone(valid.Bytes())
	tampered[len(tampered)-5] ^= 1
	cases = append(cases, tampered)
	for i, body := range cases {
		if _, err := readBundle(bytes.NewReader(body), false); err == nil {
			t.Fatalf("accepted invalid frame %d", i)
		}
	}
}

func TestLANAddressAndLifetimeBoundaries(t *testing.T) {
	for _, address := range []string{"0.0.0.0:1", "[::]:1", "8.8.8.8:443", "example.com:443", "192.168.1.2:http", "192.168.1.2:65536", "[ff02::1]:1", "[fe80::1%eth0]:1"} {
		if Address(address) == nil {
			t.Fatalf("accepted %q", address)
		}
	}
	for _, address := range []string{"127.0.0.1:0", "192.168.1.2:1234", "10.0.0.2:2345", "[fd00::1]:1234", "[::1]:1234"} {
		if err := Address(address); err != nil {
			t.Fatalf("refused %s: %v", address, err)
		}
	}
	a := bundle(t, "lineage:"+strings.Repeat("a", 32), "a")
	for _, ttl := range []time.Duration{0, -time.Second, MaxLifetime + time.Second} {
		if server, err := NewServer("127.0.0.1:0", a, filepath.Join(t.TempDir(), "incoming"), ttl); err == nil {
			server.Close()
			t.Fatal("invalid lifetime accepted")
		}
	}
}

func TestPairingFilePrivacyAndNoReplacement(t *testing.T) {
	server, _, _ := start(t, bundle(t, "lineage:"+strings.Repeat("a", 32), "a"))
	path := filepath.Join(t.TempDir(), "pairing")
	if err := WritePairing(path, server.Pairing()); err != nil {
		t.Fatal(err)
	}
	if err := WritePairing(path, server.Pairing()); err == nil {
		t.Fatal("overwrote pairing file")
	}
	got, err := ReadPairing(path)
	if err != nil || got.Secret != server.Pairing().Secret {
		t.Fatalf("pairing roundtrip: %v", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPairing(link); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPairing(path); err == nil {
		t.Fatal("accepted exposed pairing credential")
	}
}

func TestCancellationAndExpiryCloseListener(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		a := bundle(t, "lineage:"+strings.Repeat("a", 32), "a")
		ttl := time.Minute
		if expiry {
			ttl = 40 * time.Millisecond
		}
		server, err := NewServer("127.0.0.1:0", a, filepath.Join(t.TempDir(), "received"), ttl)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := server.Serve(ctx); done <- err }()
		if !expiry {
			cancel()
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("expected closed listener")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("listener did not close")
		}
		cancel()
		conn, err := net.DialTimeout("tcp", server.Pairing().Address, time.Second)
		if err == nil {
			conn.Close()
			t.Fatal("listener remains open")
		}
	}
}

func TestInterruptedAuthenticatedUploadClosesPairing(t *testing.T) {
	a := bundle(t, "lineage:"+strings.Repeat("a", 32), "a")
	server, incoming, done := start(t, a)
	conn, err := tls.Dial("tcp", server.Pairing().Address, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := hex.DecodeString(server.Pairing().Secret)
	conn.Write(secret)
	conn.Write([]byte{0, 0})
	conn.Close()
	if err := <-done; err == nil {
		t.Fatal("interrupted upload succeeded")
	}
	if _, err := os.Stat(incoming); !os.IsNotExist(err) {
		t.Fatalf("partial upload published: %v", err)
	}
}

func TestDestinationNoReplacement(t *testing.T) {
	a := bundle(t, "lineage:"+strings.Repeat("a", 32), "a")
	server, _, done := start(t, a)
	target := filepath.Join(t.TempDir(), "received")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Exchange(context.Background(), server.Pairing(), nil, target); err == nil {
		t.Fatal("existing target accepted")
	}
	// A refused destination was checked before contacting or consuming the pairing.
	if _, err := Exchange(context.Background(), server.Pairing(), nil, filepath.Join(t.TempDir(), "new")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
