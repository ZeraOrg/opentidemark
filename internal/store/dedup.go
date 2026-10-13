// SPDX-License-Identifier: Apache-2.0

package store

import "context"

// dedupKey is the context key of the insert deduplication token.
type dedupKey struct{}

// WithDedupToken attaches the deduplication token of an insert batch (SPEC §8.5 item 7). Writers
// pass it to the database (ClickHouse insert_deduplication_token) so a retried or replayed batch
// with the same token is stored once.
func WithDedupToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, dedupKey{}, token)
}

// DedupToken returns the token attached by WithDedupToken ("" when none).
func DedupToken(ctx context.Context) string {
	t, _ := ctx.Value(dedupKey{}).(string)
	return t
}
