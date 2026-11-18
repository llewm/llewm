package transport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
)

type projectRegistry struct {
	Version  int `json:"version"`
	Projects []struct {
		ID        string                       `json:"id"`
		Database  string                       `json:"database"`
		Members   []coordination.ProjectMember `json:"members"`
		MaxEvents *int64                       `json:"max_events,omitempty"`
		MaxWaits  *int                         `json:"max_waits,omitempty"`
	} `json:"projects"`
}

// OpenProjects opens every authority before serving and closes them all on any failure.
func OpenProjects(path string) (http.Handler, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	const limit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > limit {
		return nil, nil, errors.New("project registry exceeds 1 MiB")
	}
	var cfg projectRegistry
	if err := Decode(bytes.NewReader(raw), &cfg); err != nil {
		return nil, nil, err
	}
	if cfg.Version != 1 || len(cfg.Projects) == 0 || len(cfg.Projects) > 256 {
		return nil, nil, errors.New("expected registry version 1 and 1..256 projects")
	}
	stores := make(map[string]*coordination.Store)
	closeAll := func() {
		for _, s := range stores {
			s.Close()
		}
	}
	mux := http.NewServeMux()
	for _, p := range cfg.Projects {
		if !coordination.ValidBrokerID(p.ID) || p.Database == "" || stores[p.ID] != nil {
			closeAll()
			return nil, nil, errors.New("invalid or duplicate project")
		}
		db := p.Database
		if !filepath.IsAbs(db) {
			db = filepath.Join(filepath.Dir(path), db)
		}
		store, err := coordination.OpenSharedBroker(db, false, p.ID, "")
		if err != nil {
			closeAll()
			return nil, nil, fmt.Errorf("project %s: %w", p.ID, err)
		}
		stores[p.ID] = store
		maxEvents, maxWaits := int64(100000), 32
		if p.MaxEvents != nil {
			maxEvents = *p.MaxEvents
		}
		if p.MaxWaits != nil {
			maxWaits = *p.MaxWaits
		}
		if err := store.ConfigureProject(p.Members, maxEvents, maxWaits); err != nil {
			closeAll()
			return nil, nil, fmt.Errorf("project %s: %w", p.ID, err)
		}
		base := "/v1/projects/" + p.ID
		handler := brokerRoutes(store, base)
		mux.Handle(base, handler)
		mux.Handle(base+"/", handler)
	}
	return mux, closeAll, nil
}
