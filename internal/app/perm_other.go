// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package app

// restrictFile is a no-op where the file mode (0600) already restricts access.
func restrictFile(string) error { return nil }
