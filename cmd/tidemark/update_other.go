// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package main

import (
	"context"
	"errors"
	"log/slog"
)

// updateInstall exists only on Linux; Windows installs through the TidemarkUpdate service.
func updateInstall(context.Context, *slog.Logger) error {
	return errors.New("update install is only available on Linux")
}
