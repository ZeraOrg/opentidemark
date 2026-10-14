// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"strings"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
)

// streamCond returns the condition limiting the `stream` column to scope (SPEC §15.10) and
// its arguments: "1" for all streams, "0" for none, else `stream IN (?, …)` with every name as
// an argument. An unset scope fails with store.ErrNoScope.
func streamCond(scope model.StreamScope) (string, []any, error) {
	switch {
	case !scope.IsSet():
		return "", nil, store.ErrNoScope
	case scope.All():
		return "1", nil, nil
	case scope.Empty():
		return "0", nil, nil
	}
	names := scope.Names()
	args := make([]any, len(names))
	for i, n := range names {
		args[i] = n
	}
	return "stream IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ") + ")", args, nil
}

// concatArgs returns a new slice with the arguments of every part in order.
func concatArgs(parts ...[]any) []any {
	var out []any
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// checkFilter rejects filters compiled without a stream scope.
func checkFilter(f store.Filter) error {
	if !f.Streams.IsSet() {
		return store.ErrNoScope
	}
	return nil
}
