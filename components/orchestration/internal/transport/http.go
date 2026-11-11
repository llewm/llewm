// Package transport exposes only the manual coordination protocol. It never
// accepts shell commands, provider credentials, callbacks or artifact uploads.
package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
)

func Handler(store *coordination.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/commands", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, coordination.MaxCommandBytes)
		defer r.Body.Close()
		var c coordination.Command
		if err := Decode(r.Body, &c); err != nil {
			writeResponse(w, nil, errors.New("invalid command JSON, unknown field, trailing data or body too large"))
			return
		}
		result, err := store.Execute(r.Context(), bearerToken(r), c)
		writeResponse(w, result, err)
	})
	mux.HandleFunc("GET /v1/plans", func(w http.ResponseWriter, r *http.Request) {
		result, err := store.List(r.Context(), bearerToken(r))
		writeResponse(w, result, err)
	})
	mux.HandleFunc("POST /v1/queue", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, coordination.MaxCommandBytes)
		defer r.Body.Close()
		var query coordination.QueueRequest
		if err := Decode(r.Body, &query); err != nil {
			writeResponse(w, nil, errors.New("invalid queue JSON"))
			return
		}
		result, err := store.Queue(r.Context(), bearerToken(r), query)
		writeResponse(w, result, err)
	})
	mux.HandleFunc("GET /v1/plans/{id}", func(w http.ResponseWriter, r *http.Request) {
		result, err := store.Read(r.Context(), bearerToken(r), r.PathValue("id"))
		writeResponse(w, result, err)
	})
	mux.HandleFunc("GET /v1/plans/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var after int64
		var err error
		if value := r.URL.Query().Get("after"); value != "" {
			after, err = strconv.ParseInt(value, 10, 64)
		}
		if err != nil {
			writeResponse(w, nil, errors.New("after must be an integer"))
			return
		}
		result, err := store.History(r.Context(), bearerToken(r), r.PathValue("id"), after)
		writeResponse(w, result, err)
	})
	return mux
}

func Decode(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

func Loopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func coordinatorURL(endpoint string) (*url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("endpoint must be an HTTP(S) origin without credentials or path")
	}
	loopback := net.ParseIP(u.Hostname())
	local := loopback != nil && loopback.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return nil, errors.New("remote coordinators require HTTPS; HTTP is restricted to loopback IPs")
	}
	return u, nil
}

func ValidateEndpoint(endpoint string) error { _, err := coordinatorURL(endpoint); return err }

// Request refuses redirects and cleartext non-loopback connections. Server
// authentication uses the platform's TLS roots, with no insecure bypass flag.
func Request(ctx context.Context, endpoint, token, method, path string, body, caPEM []byte) ([]byte, error) {
	u, err := coordinatorURL(endpoint)
	if err != nil {
		return nil, err
	}
	relative, err := url.Parse(path)
	if err != nil || relative.IsAbs() || relative.Host != "" || !strings.HasPrefix(relative.Path, "/v1/") {
		return nil, errors.New("invalid API path")
	}
	u.Path = relative.Path
	u.RawQuery = relative.RawQuery
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("coordinator redirects are refused") }}
	if len(caPEM) > 0 {
		if len(caPEM) > 1<<20 || bytes.Contains(caPEM, []byte("PRIVATE KEY")) {
			return nil, errors.New("CA file must contain only bounded public certificates")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, errors.New("cannot load platform TLS trust")
		}
		if !roots.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("CA file contains no usable public certificates")
		}
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, errors.New("unsupported default HTTP transport")
		}
		tr := base.Clone()
		tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		client.Transport = tr
		defer tr.CloseIdleConnections()
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("coordinator request failed (check endpoint, reachability and TLS trust)")
	}
	defer response.Body.Close()
	const maxResponse = 16 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponse {
		return nil, errors.New("coordinator response too large")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coordinator returned %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func writeResponse(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, coordination.ErrForbidden) {
			status = http.StatusForbidden
		}
		if errors.Is(err, coordination.ErrNotFound) {
			status = http.StatusNotFound
		}
		if errors.Is(err, coordination.ErrConflict) {
			status = http.StatusConflict
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(value)
}
func bearerToken(r *http.Request) string {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}
