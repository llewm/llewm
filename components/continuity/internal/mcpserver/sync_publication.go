package mcpserver

import (
	"context"
	"fmt"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/memsync"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SyncPublicationInput struct {
	Refs           []string `json:"refs"`
	EnvelopeSHA256 string   `json:"envelope_sha256,omitempty" jsonschema:"exact bytes; one ref only"`
}

func registerSyncPublication(server *mcp.Server, application *app.App) {
	addTool(server, &mcp.Tool{Name: "continuity_sync_publication", Description: "Check publication in fetched Git history, not peer receipt; no publish/import."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input SyncPublicationInput) (*mcp.CallToolResult, memsync.PublicationCheck, error) {
			settings := application.Config.Sync
			if settings.Remote == "" {
				return nil, memsync.PublicationCheck{}, fmt.Errorf("no Git history remote configured")
			}
			clone := settings.Clone
			if clone == "" {
				var err error
				clone, err = memsync.DefaultClone(settings.Remote)
				if err != nil {
					return nil, memsync.PublicationCheck{}, err
				}
			}
			s := memsync.Syncer{DB: application.DB, ProjectID: application.ProjectID, Remote: settings.Remote, Clone: clone, Askpass: settings.Askpass, SSHKey: settings.SSHKey, NoPrompt: true}
			value, err := s.CheckPublication(ctx, input.Refs, input.EnvelopeSHA256)
			return nil, value, err
		})
}
