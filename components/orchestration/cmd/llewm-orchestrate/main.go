package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
	"github.com/llewm/llewm/components/orchestration/internal/transport"
)

const help = `llewm-orchestrate: manually coordinate shared agent work

Operator commands (local coordinator database):
  init   --db PATH
  member --db PATH --id ENGINEER --permissions plan,accept,assign,review,work --token-file PATH
  revoke --db PATH --id ENGINEER
  serve  --db PATH [--listen 127.0.0.1:9467] [--tls-cert PATH --tls-key PATH]

Engineer commands (authenticate with a private token file):
  list    --endpoint URL --token-file PATH
  show    --endpoint URL --token-file PATH --plan ID [--history --after SEQUENCE]
  request --endpoint URL --token-file PATH --file COMMAND.json

Prepare an initial proposal from a public Continuity plan export (no network):
  prepare --file SOURCE.json --plan ID --request-id ID

request sends the versioned JSON command exactly once. Reuse its request_id and
unchanged body after a network failure. Actions: propose, accept-plan, assign,
claim, resume, release, submit, review. See components/orchestration/README.md.
Tokens are issued to files, never printed. Provider accounts remain local.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Fprint(out, help)
		return nil
	}
	cmd := args[0]
	flags := flag.NewFlagSet(cmd, flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db", "", "private orchestration database path")
	id := flags.String("id", "", "engineer identifier")
	permissions := flags.String("permissions", "", "comma-separated permissions; empty means read-only")
	tokenFile := flags.String("token-file", "", "private coordinator credential file")
	listen := flags.String("listen", "127.0.0.1:9467", "listen address")
	cert := flags.String("tls-cert", "", "TLS certificate")
	key := flags.String("tls-key", "", "TLS private key")
	endpoint := flags.String("endpoint", "", "coordinator origin URL")
	plan := flags.String("plan", "", "plan identifier")
	history := flags.Bool("history", false, "show a page of the plan's mutation ledger")
	after := flags.Int64("after", 0, "history cursor from next_after")
	file := flags.String("file", "", "JSON command file")
	requestID := flags.String("request-id", "", "unique idempotency key for a prepared proposal")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	ctx := context.Background()
	switch cmd {
	case "prepare":
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer f.Close()
		body, err := io.ReadAll(io.LimitReader(f, coordination.MaxCommandBytes+1))
		if err != nil {
			return err
		}
		command, err := coordination.PrepareContinuity(body, *plan, *requestID)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(command)
	case "init", "member", "revoke", "serve":
		if *dbPath == "" {
			return errors.New("--db is required")
		}
		store, err := coordination.Open(*dbPath, cmd == "init")
		if err != nil {
			return err
		}
		defer store.Close()
		switch cmd {
		case "init":
			fmt.Fprintln(out, "Initialized private orchestration database.")
			return nil
		case "member":
			if *tokenFile == "" {
				return errors.New("--token-file is required (must not already exist)")
			}
			f, err := os.OpenFile(*tokenFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			ok := false
			defer func() {
				f.Close()
				if !ok {
					os.Remove(*tokenFile)
				}
			}()
			var perms []string
			if *permissions != "" {
				perms = strings.Split(*permissions, ",")
			}
			token, err := store.Enroll(ctx, *id, perms)
			if err != nil {
				return err
			}
			if _, err = f.WriteString(token + "\n"); err != nil {
				return errors.New("credential file write failed; re-enroll to rotate the unavailable credential")
			}
			if err = f.Sync(); err != nil {
				return err
			}
			if err = f.Close(); err != nil {
				return err
			}
			ok = true
			fmt.Fprintln(out, "Enrolled engineer; credential saved to the private token file. Previous credential revoked.")
			return nil
		case "revoke":
			if err := store.Revoke(ctx, *id); err != nil {
				return err
			}
			fmt.Fprintln(out, "Revoked engineer credential.")
			return nil
		case "serve":
			if (*cert == "") != (*key == "") {
				return errors.New("provide both --tls-cert and --tls-key")
			}
			if *cert == "" && !transport.Loopback(*listen) {
				return errors.New("non-loopback listeners require TLS")
			}
			server := &http.Server{Addr: *listen, Handler: transport.Handler(store), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8192}
			stop, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer cancel()
			done := make(chan struct{})
			defer close(done)
			go func() {
				select {
				case <-stop.Done():
					shutdown, c := context.WithTimeout(context.Background(), 5*time.Second)
					defer c()
					server.Shutdown(shutdown)
				case <-done:
				}
			}()
			fmt.Fprintln(out, "Serving manual orchestration coordinator at", *listen)
			if *cert != "" {
				err = server.ListenAndServeTLS(*cert, *key)
			} else {
				err = server.ListenAndServe()
			}
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		}
	case "list", "show", "request":
		token, err := readPrivateToken(*tokenFile)
		if err != nil {
			return err
		}
		method, path := "GET", "/v1/plans"
		var body []byte
		if cmd == "show" {
			if *plan == "" || strings.ContainsAny(*plan, "/?#%") {
				return errors.New("--plan must name one plan")
			}
			path += "/" + *plan
			if *history {
				path += fmt.Sprintf("/events?after=%d", *after)
			}
		}
		if cmd == "request" {
			f, err := os.Open(*file)
			if err != nil {
				return err
			}
			defer f.Close()
			body, err = io.ReadAll(io.LimitReader(f, coordination.MaxCommandBytes+1))
			if err != nil {
				return err
			}
			if len(body) > coordination.MaxCommandBytes {
				return errors.New("command too large")
			}
			method, path = "POST", "/v1/commands"
		}
		data, err := transport.Request(ctx, *endpoint, token, method, path, body)
		if err != nil {
			return err
		}
		_, err = out.Write(data)
		return err
	}
	return errors.New("unknown command; use --help")
}

func readPrivateToken(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("credential file must be private (mode 0600)")
	}
	b, err := io.ReadAll(io.LimitReader(f, 256))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if len(token) != 71 || !strings.HasPrefix(token, "lworch_") {
		return "", errors.New("invalid coordinator credential file")
	}
	return token, nil
}
