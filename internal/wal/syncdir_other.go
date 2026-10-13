// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package wal

import (
	"fmt"
	"os"
)

// syncDir fsyncs a directory so a newly created segment survives a crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open wal dir: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync wal dir: %w", err)
	}
	return nil
}
