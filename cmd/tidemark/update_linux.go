// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"

	"github.com/ZeraOrg/opentidemark/internal/app"
)

// updateInstall runs the update helper started by tidemark-update.service (SPEC §17.7). The
// service restarts itself when it sees the new state, so the helper restarts nothing.
func updateInstall(ctx context.Context, logger *slog.Logger) error {
	return app.RunUpdateHelper(ctx, version, nil, logger)
}
