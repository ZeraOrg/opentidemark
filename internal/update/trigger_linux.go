// SPDX-License-Identifier: Apache-2.0

package update

// StartHelper does nothing on Linux: tidemark-update.path starts the helper when request.json is
// written (SPEC §17.7).
func StartHelper() error { return nil }

// helperServiceRegistered is not used on Linux (the path unit is checked instead).
func helperServiceRegistered() bool { return false }
