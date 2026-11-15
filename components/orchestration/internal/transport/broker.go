package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
)

// BrokerHandler exposes messages for the opened broker scope, never team claims.
func BrokerHandler(store *coordination.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/broker", func(w http.ResponseWriter, r *http.Request) {
		out, err := store.BrokerInfo(r.Context(), bearerToken(r))
		writeResponse(w, out, err)
	})
	mux.HandleFunc("POST /v1/broker/commands", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, coordination.MaxCommandBytes)
		defer r.Body.Close()
		var c coordination.BrokerCommand
		if err := Decode(r.Body, &c); err != nil {
			writeResponse(w, nil, errors.New("invalid broker JSON, unknown field, trailing data or body too large"))
			return
		}
		out, err := store.BrokerExecute(r.Context(), bearerToken(r), c)
		writeResponse(w, out, err)
	})
	mux.HandleFunc("POST /v1/broker/pull", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		defer r.Body.Close()
		var q coordination.BrokerQuery
		if err := Decode(r.Body, &q); err != nil {
			writeResponse(w, nil, errors.New("invalid broker query"))
			return
		}
		out, err := store.BrokerPull(r.Context(), bearerToken(r), q)
		writeResponse(w, out, err)
	})
	return mux
}

// ValidateLocalEndpoint refuses remote destinations for local observations.
func ValidateLocalEndpoint(endpoint string) error {
	u, err := coordinatorURL(endpoint)
	if err != nil {
		return err
	}
	if !Loopback(u.Host) {
		return errors.New("local broker requires a literal loopback endpoint with explicit port")
	}
	return nil
}

// Shared clients always require TLS, including loopback connections to a proxy.
// A proxy's server-side loopback upstream is configured independently.
func ValidateSharedBrokerEndpoint(endpoint string) error {
	u, err := coordinatorURL(endpoint)
	if err != nil {
		return err
	}
	if u.Scheme != "https" {
		return errors.New("shared broker requires HTTPS")
	}
	return nil
}

// SharedBrokerRequest checks the destination's scope and project before sending
// session or message content. TLS and the explicitly configured origin remain
// the server authentication boundary; labels alone do not establish trust.
func SharedBrokerRequest(ctx context.Context, endpoint, token, project, method, path string, body, caPEM []byte) ([]byte, error) {
	if err := ValidateSharedBrokerEndpoint(endpoint); err != nil {
		return nil, err
	}
	if project == "" {
		return nil, errors.New("shared broker requires an expected project")
	}
	if path != "/v1/broker" && path != "/v1/broker/commands" && path != "/v1/broker/pull" {
		return nil, errors.New("invalid shared broker API path")
	}
	data, err := Request(ctx, endpoint, token, http.MethodGet, "/v1/broker", nil, caPEM)
	if err != nil {
		return nil, err
	}
	var identity coordination.BrokerIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return nil, errors.New("invalid shared broker identity")
	}
	if identity.SchemaVersion != 1 || identity.Scope != "shared" || identity.Project != project || identity.ID == "" {
		return nil, errors.New("shared broker scope or project mismatch")
	}
	if method == http.MethodGet && path == "/v1/broker" {
		return data, nil
	}
	return Request(ctx, endpoint, token, method, path, body, caPEM)
}
