// SPDX-License-Identifier: Apache-2.0

package wal

// syncDir is a no-op on Windows: NTFS journals directory entries and directories cannot be
// opened for FlushFileBuffers through os.File.
func syncDir(string) error { return nil }
