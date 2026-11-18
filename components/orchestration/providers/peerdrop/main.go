package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/llewm/llewm/components/orchestration/internal/connectionprofile"
	"github.com/llewm/llewm/components/orchestration/internal/coordination"
	"github.com/llewm/llewm/components/orchestration/internal/transport"
	"github.com/llewm/llewm/components/privatestorage"
)

type handoff struct {
	Profile    connectionprofile.Profile `json:"profile"`
	Credential string                    `json:"credential"`
}

func (h *handoff) verify(ctx context.Context) error {
	if err := h.Profile.Validate(); err != nil {
		return err
	}
	expiry, err := coordination.CredentialExpiry(h.Credential)
	if err != nil || !expiry.After(time.Now()) {
		return errors.New("pairing requires a current expiring credential")
	}
	_, err = transport.PinnedSharedBrokerRequest(ctx, h.Profile.Endpoint, h.Credential, h.Profile.Project, h.Profile.Authority, http.MethodGet, "/v1/broker", nil, []byte(h.Profile.CACertificates))
	if err != nil {
		return errors.New("credential did not verify against the pinned project authority")
	}
	return nil
}

func loadOffer(ctx context.Context, profilePath, tokenPath string) ([]byte, error) {
	p, err := connectionprofile.Read(profilePath)
	if err != nil {
		return nil, err
	}
	f, err := privatestorage.Open(tokenPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := privatestorage.CheckFile(f); err != nil {
		return nil, err
	}
	token, err := io.ReadAll(io.LimitReader(f, 257))
	if err != nil {
		return nil, err
	}
	if len(token) > 256 {
		return nil, errors.New("invalid pairing credential size")
	}
	h := handoff{p, strings.TrimSpace(string(token))}
	if err := h.verify(ctx); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(h)
	if len(raw) > maxPacket {
		return nil, errors.New("profile too large for PeerDrop pairing")
	}
	return raw, err
}

// Create both files exclusively in a private directory. Clean up only files
// this invocation created if any write fails.
func savePair(path string, h handoff) error {
	parent := filepath.Dir(path)
	if err := privatestorage.MkdirAll(parent); err != nil {
		return err
	}
	if err := privatestorage.CheckDir(parent); err != nil {
		return err
	}
	f, err := privatestorage.Create(path + ".token")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(path + ".token")
		}
	}()
	if _, err = f.WriteString(h.Credential + "\n"); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := connectionprofile.Write(path, h.Profile); err != nil {
		return err
	}
	ok = true
	return nil
}

func confirm(ctx context.Context, l *link, code string) error {
	fmt.Printf("Compare with the other developer by voice or on their screen: %s\nType the matching code to approve: ", code)
	input := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(io.LimitReader(os.Stdin, 64)).ReadString('\n')
		input <- strings.TrimSpace(line)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.ended:
		return errors.New("peer disconnected")
	case line := <-input:
		if line != code {
			return errors.New("pairing declined")
		}
		return nil
	}
}

func run(args []string) error {
	if len(args) == 0 || (args[0] != "offer" && args[0] != "receive") {
		return errors.New("use offer --profile FILE --token-file GUEST_TOKEN or receive --room CODE --profile NEW_PRIVATE_PATH")
	}
	host := args[0] == "offer"
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	provider := fs.String("provider", "https://peerdrop.fluxwall.com", "PeerDrop HTTPS origin")
	profile := fs.String("profile", "", "host's public profile or new guest profile destination")
	token := fs.String("token-file", "", "host's private expiring guest credential file")
	room := fs.String("room", "", "room code from the host")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *profile == "" || (host && (*token == "" || *room != "")) || (!host && (*token != "" || !roomCode.MatchString(*room))) {
		return errors.New("invalid pairing arguments")
	}
	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, timeout := context.WithTimeout(stop, 5*time.Minute)
	defer timeout()
	var raw []byte
	var err error
	if host {
		raw, err = loadOffer(ctx, *profile, *token)
		if err != nil {
			return err
		}
	} else {
		for _, p := range []string{*profile, *profile + ".token"} {
			if _, err := os.Lstat(p); !os.IsNotExist(err) {
				return errors.New("guest profile and token destinations must be new")
			}
		}
	}
	l, err := connect(ctx, *provider, *room, host, func(code string) { fmt.Println("PeerDrop room:", code) })
	if err != nil {
		return err
	}
	defer l.pc.Close()
	code, err := comparison(ctx, l, host)
	if err != nil {
		return err
	}
	if err := confirm(ctx, l, code); err != nil {
		return err
	}
	if host {
		ready, err := l.receive(ctx)
		if err != nil {
			return err
		}
		if string(ready) != "approved" {
			return errors.New("guest did not approve")
		}
		if err := l.channel.Send(raw); err != nil {
			return err
		}
		saved, err := l.receive(ctx)
		if err != nil {
			return errors.New("guest receipt uncertain; verify before reissuing")
		}
		if string(saved) != "saved" {
			return errors.New("expected guest receipt")
		}
		fmt.Println("Guest verified the broker and saved the profile and credential.")
		return nil
	}
	if err := l.channel.SendText("approved"); err != nil {
		return err
	}
	data, err := l.receive(ctx)
	if err != nil {
		return err
	}
	var h handoff
	if err := transport.Decode(bytes.NewReader(data), &h); err != nil {
		return errors.New("invalid handoff")
	}
	if err := h.verify(ctx); err != nil {
		return err
	}
	if err := savePair(*profile, h); err != nil {
		return err
	}
	fmt.Println("Verified broker and saved profile and private credential.")
	if err := l.channel.SendText("saved"); err != nil {
		return errors.New("files saved; acknowledgment uncertain")
	}
	select {
	case <-l.ended:
	case <-ctx.Done():
		return errors.New("files saved; host receipt uncertain")
	}
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
