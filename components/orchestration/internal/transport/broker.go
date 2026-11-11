package transport

import (
	"errors"
	"net/http"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
)

// BrokerHandler exposes only machine-local messages, never team claims.
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
