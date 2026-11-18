package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
)

// BrokerHandler exposes messages for the opened broker scope, never team claims.
func BrokerHandler(store *coordination.Store) http.Handler {
	return brokerRoutes(store, "/v1/broker")
}

func brokerRoutes(store *coordination.Store, base string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		out, err := store.BrokerInfo(r.Context(), bearerToken(r))
		writeResponse(w, out, err)
	})
	mux.HandleFunc("POST "+base+"/commands", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("POST "+base+"/pull", func(w http.ResponseWriter, r *http.Request) {
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
	_, _, err := sharedDestination(endpoint)
	return err
}

// Accept either a dedicated HTTPS origin or the canonical address of one registered project.
func sharedDestination(endpoint string) (origin, project string, err error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.RawPath != "" || u.ForceQuery || strings.ContainsAny(endpoint, "?#") {
		return "", "", errors.New("shared broker requires an HTTPS origin or project URL")
	}
	if u.Path != "" && u.Path != "/" {
		var ok bool
		project, ok = strings.CutPrefix(u.Path, "/v1/projects/")
		if !ok || !coordination.ValidBrokerID(project) {
			return "", "", errors.New("invalid shared project URL")
		}
	}
	u.Path = ""
	if _, err = coordinatorURL(u.String()); err != nil {
		return "", "", err
	}
	return u.String(), project, nil
}

// SharedBrokerRequest checks the destination's scope and project before sending
// session or message content. TLS and the explicitly configured origin remain
// the server authentication boundary; labels alone do not establish trust.
func SharedBrokerRequest(ctx context.Context, endpoint, token, project, method, path string, body, caPEM []byte) ([]byte, error) {
	return PinnedSharedBrokerRequest(ctx, endpoint, token, project, "", method, path, body, caPEM)
}

func PinnedSharedBrokerRequest(ctx context.Context, endpoint, token, project, authority, method, path string, body, caPEM []byte) ([]byte, error) {
	origin, routedProject, err := sharedDestination(endpoint)
	if err != nil {
		return nil, err
	}
	if project == "" {
		return nil, errors.New("shared broker requires an expected project")
	}
	if path != "/v1/broker" && path != "/v1/broker/commands" && path != "/v1/broker/pull" {
		return nil, errors.New("invalid shared broker API path")
	}
	base := "/v1/broker"
	if routedProject != "" {
		if routedProject != project {
			return nil, errors.New("endpoint and expected project differ")
		}
		base = "/v1/projects/" + project
	}
	data, err := Request(ctx, origin, token, http.MethodGet, base, nil, caPEM)
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
	if authority != "" && identity.ID != authority {
		return nil, errors.New("shared broker authority differs from profile")
	}
	if method == http.MethodGet && path == "/v1/broker" {
		return data, nil
	}
	return Request(ctx, origin, token, method, base+strings.TrimPrefix(path, "/v1/broker"), body, caPEM)
}
