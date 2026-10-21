// SPDX-License-Identifier: Apache-2.0

package update

import "path/filepath"

// HelperPaths are the fixed locations of the helper protocol (SPEC §17.7). They do not depend on
// the server's configuration, so the service and the helper always agree.
type HelperPaths struct {
	Binary  string // the installed binary the helper replaces
	Staging string // written by the service: request, release files, confirmed.json
	Work    string // the helper's own directory: lock, state.json, temporary copies
}

// StatePath is the helper's state.json.
func (p HelperPaths) StatePath() string { return filepath.Join(p.Work, StateFile) }
