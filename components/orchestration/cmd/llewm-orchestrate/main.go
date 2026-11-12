package main

import (
	"context"
	"encoding/hex"
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
	"syscall"
	"time"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
	"github.com/llewm/llewm/components/orchestration/internal/transport"
	"github.com/llewm/llewm/components/orchestration/internal/worker"
	"github.com/llewm/llewm/components/privatestorage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const help = `llewm-orchestrate: manually coordinate shared agent work

Operator commands (local coordinator database):
  broker-init --db PATH --project ID --machine LABEL
  broker-member --db PATH --id ENGINEER --permissions work --token-file PATH
  broker-revoke --db PATH --id ENGINEER
  broker-serve --db PATH [--listen 127.0.0.1:9468]
  init   --db PATH
  member --db PATH --id ENGINEER --permissions plan,accept,assign,review,work --token-file PATH
  revoke --db PATH --id ENGINEER
  serve  --db PATH [--listen 127.0.0.1:9467] [--tls-cert PATH --tls-key PATH]

Engineer commands (authenticate with a private token file):
  broker-info --endpoint URL --token-file PATH
  broker-request --endpoint URL --token-file PATH --file COMMAND.json
  broker-pull --endpoint URL --token-file PATH --file QUERY.json
  broker-mcp --endpoint URL --token-file PATH (--session ID | --session-env VAR) --agent LABEL [--task ID]
  broker-hook --endpoint URL --token-file PATH --agent LABEL (--cursor-file PATH | --cursor-dir DIR) [--task ID]
              [--state active|ended] (reads session_id and hook_event_name from JSON stdin)
  list    --endpoint URL --token-file PATH
  show    --endpoint URL --token-file PATH --plan ID [--history --after SEQUENCE]
  request --endpoint URL --token-file PATH --file COMMAND.json
  credential-import --file RECEIVED_TOKEN --token-file NEW_PRIVATE_TOKEN
  mcp     --endpoint URL --token-file PATH --allow-plan ID [--allow-plan ID ...]
          --machine LABEL --agent LABEL [--capability LABEL ...]

Prepare an initial proposal from a public Continuity plan export (no network):
  prepare --file SOURCE.json --plan ID --request-id ID [--job-file ROUTING.json]

request sends the versioned JSON command exactly once. Reuse its request_id and
unchanged body after a network failure. Actions: propose, accept-plan, assign,
claim, resume, release, pause, unblock, submit, review. See components/orchestration/README.md.
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
	caFile := flags.String("ca-file", "", "optional public coordinator CA bundle; trust applies to this client only")
	plan := flags.String("plan", "", "plan identifier")
	history := flags.Bool("history", false, "show a page of the plan's mutation ledger")
	after := flags.Int64("after", 0, "history cursor from next_after")
	file := flags.String("file", "", "JSON command file")
	jobFile := flags.String("job-file", "", "optional reviewed routing JSON pinned to the exported plan revision")
	requestID := flags.String("request-id", "", "unique idempotency key for a prepared proposal")
	machine := flags.String("machine", "", "caller-asserted machine label for MCP claims")
	agent := flags.String("agent", "", "caller-asserted agent label for MCP claims")
	project := flags.String("project", "", "stable project identifier for local broker creation")
	session := flags.String("session", "", "actual client session identifier")
	task := flags.String("task", "", "advisory task identifier")
	cursorFile := flags.String("cursor-file", "", "private per-session hook cursor file")
	cursorDir := flags.String("cursor-dir", "", "private directory for hook cursors derived from each envelope's session_id and the task")
	sessionEnv := flags.String("session-env", "", "environment variable holding the actual client session identifier, when --session is not given")
	state := flags.String("state", "active", "hook session observation: active or ended")
	var allowedPlans, capabilities []string
	flags.Func("allow-plan", "explicit plan permitted for this MCP client (repeatable)", func(s string) error { allowedPlans = append(allowedPlans, s); return nil })
	flags.Func("capability", "explicit local worker capability (repeatable)", func(s string) error { capabilities = append(capabilities, s); return nil })
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	ctx := context.Background()
	var caPEM []byte
	if *caFile != "" {
		f, err := os.Open(*caFile)
		if err != nil {
			return err
		}
		caPEM, err = io.ReadAll(io.LimitReader(f, 1<<20+1))
		f.Close()
		if err != nil {
			return err
		}
		if len(caPEM) > 1<<20 {
			return errors.New("CA file exceeds 1 MiB")
		}
	}
	switch cmd {
	case "credential-import":
		return importCredential(*file, *tokenFile, out)
	case "broker-hook":
		return brokerHook(ctx, *endpoint, *tokenFile, *agent, *task, *state, *cursorFile, *cursorDir, os.Stdin, out)
	case "broker-mcp":
		token, err := readPrivateToken(*tokenFile)
		if err != nil {
			return err
		}
		if *session == "" && *sessionEnv != "" {
			if *session = os.Getenv(*sessionEnv); *session == "" {
				return fmt.Errorf("--session-env %s is unset or empty", *sessionEnv)
			}
		}
		server, err := worker.NewBrokerServer(worker.BrokerConfig{Endpoint: *endpoint, Token: token, SessionID: *session, TaskID: *task, Agent: *agent})
		if err != nil {
			return err
		}
		return server.Run(ctx, &mcp.StdioTransport{})
	case "mcp":
		token, err := readPrivateToken(*tokenFile)
		if err != nil {
			return err
		}
		server, err := worker.NewServer(worker.Config{Endpoint: *endpoint, Token: token, PlanIDs: allowedPlans, Capabilities: capabilities, Machine: *machine, Agent: *agent, CAPEM: caPEM})
		if err != nil {
			return err
		}
		return server.Run(ctx, &mcp.StdioTransport{})
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
		if *jobFile != "" {
			f, err := os.Open(*jobFile)
			if err != nil {
				return err
			}
			routing, err := io.ReadAll(io.LimitReader(f, coordination.MaxCommandBytes+1))
			f.Close()
			if err != nil {
				return err
			}
			command, err = coordination.ApplyRouting(command, routing)
			if err != nil {
				return err
			}
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(command)
	case "init", "member", "revoke", "serve", "broker-init", "broker-member", "broker-revoke", "broker-serve":
		if *dbPath == "" {
			return errors.New("--db is required")
		}
		var store *coordination.Store
		var err error
		localBroker := strings.HasPrefix(cmd, "broker-")
		if localBroker {
			store, err = coordination.OpenBroker(*dbPath, cmd == "broker-init", *project, *machine)
		} else {
			store, err = coordination.Open(*dbPath, cmd == "init")
		}
		if err != nil {
			return err
		}
		defer store.Close()
		switch cmd {
		case "init", "broker-init":
			fmt.Fprintln(out, "Initialized private orchestration database.")
			return nil
		case "member", "broker-member":
			if *tokenFile == "" {
				return errors.New("--token-file is required (must not already exist)")
			}
			f, err := privatestorage.Create(*tokenFile)
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
		case "revoke", "broker-revoke":
			if err := store.Revoke(ctx, *id); err != nil {
				return err
			}
			fmt.Fprintln(out, "Revoked engineer credential.")
			return nil
		case "serve", "broker-serve":
			if localBroker {
				explicitListen := false
				flags.Visit(func(f *flag.Flag) {
					if f.Name == "listen" {
						explicitListen = true
					}
				})
				if !explicitListen {
					*listen = "127.0.0.1:9468"
				}
				if !transport.Loopback(*listen) {
					return errors.New("local broker must listen on a literal loopback address")
				}
			}
			if (*cert == "") != (*key == "") {
				return errors.New("provide both --tls-cert and --tls-key")
			}
			if *cert == "" && !transport.Loopback(*listen) {
				return errors.New("non-loopback listeners require TLS")
			}
			handler := transport.Handler(store)
			if localBroker {
				handler = transport.BrokerHandler(store)
			}
			server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8192}
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
	case "list", "show", "request", "broker-info", "broker-request", "broker-pull":
		if strings.HasPrefix(cmd, "broker-") {
			if err := transport.ValidateLocalEndpoint(*endpoint); err != nil {
				return err
			}
		}
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
		if cmd == "request" || cmd == "broker-request" || cmd == "broker-pull" {
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
		if cmd == "broker-info" {
			path = "/v1/broker"
		}
		if cmd == "broker-request" {
			path = "/v1/broker/commands"
		}
		if cmd == "broker-pull" {
			path = "/v1/broker/pull"
		}
		data, err := transport.Request(ctx, *endpoint, token, method, path, body, caPEM)
		if err != nil {
			return err
		}
		_, err = out.Write(data)
		return err
	}
	return errors.New("unknown command; use --help")
}

func readPrivateToken(path string) (string, error) {
	f, err := privatestorage.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := privatestorage.CheckFile(f); err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(f, 257))
	if err != nil {
		return "", err
	}
	return parseToken(b)
}

func parseToken(b []byte) (string, error) {
	token := strings.TrimSpace(string(b))
	if len(b) > 256 || len(token) != 71 || !strings.HasPrefix(token, "lworch_") {
		return "", errors.New("invalid coordinator credential file")
	}
	if _, err := hex.DecodeString(token[7:]); err != nil {
		return "", errors.New("invalid coordinator credential file")
	}
	return token, nil
}

// A received file may inherit ordinary Downloads permissions on Windows. Copy
// it into a newly created private object before using it; never relax normal
// credential reads or overwrite an existing credential.
func importCredential(source, target string, out io.Writer) error {
	if source == "" || target == "" {
		return errors.New("--file and --token-file are required")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("received credential must be a regular file")
	}
	f, err := privatestorage.Open(source)
	if err != nil {
		return err
	}
	info, err = f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return errors.New("received credential must be a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, 257))
	f.Close()
	if err != nil {
		return err
	}
	token, err := parseToken(b)
	if err != nil {
		return err
	}
	parent := filepath.Dir(target)
	if err := privatestorage.MkdirAll(parent); err != nil {
		return err
	}
	if err := privatestorage.CheckDir(parent); err != nil {
		return err
	}
	dest, err := privatestorage.Create(target)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		dest.Close()
		if !ok {
			os.Remove(target)
		}
	}()
	if _, err := dest.WriteString(token + "\n"); err != nil {
		return errors.New("private credential import write failed")
	}
	if err := dest.Sync(); err != nil {
		return err
	}
	if err := dest.Close(); err != nil {
		return err
	}
	verified, err := readPrivateToken(target)
	if err != nil {
		return err
	}
	if verified != token {
		return errors.New("private credential import verification failed")
	}
	ok = true
	fmt.Fprintln(out, "Imported credential into a new verified private file. Securely remove the transfer copy after authentication succeeds.")
	return nil
}
