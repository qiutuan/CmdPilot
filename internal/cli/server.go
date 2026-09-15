package cli

import (
	"github.com/qiutuan/CmdPilot/internal/config"
	"github.com/qiutuan/CmdPilot/internal/db"
	"github.com/qiutuan/CmdPilot/internal/knowledge"
	"github.com/qiutuan/CmdPilot/internal/logx"
	"github.com/qiutuan/CmdPilot/internal/server"
)

// NewServer wires a daemon server for the CLI's `daemon start`.
func NewServer(store *db.DB, kb *knowledge.DB, cfg *config.Config, log *logx.Logger) *server.Server {
	return server.New(store, kb, cfg, log)
}
