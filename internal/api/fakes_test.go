// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// fakeAdmin implements the stream storage ports in memory.
type fakeAdmin struct {
	stats    []store.StreamStats
	mappings []store.FieldMapping
	purges   []store.PurgeRequest
}

func (f *fakeAdmin) Stats(context.Context) ([]store.StreamStats, error) { return f.stats, nil }

func (f *fakeAdmin) Mappings(context.Context, model.StreamScope, string) ([]store.FieldMapping, error) {
	return f.mappings, nil
}

func (f *fakeAdmin) DeleteStream(context.Context, string) error                 { return nil }
func (f *fakeAdmin) AddIndexedField(context.Context, store.IndexedField) error  { return nil }
func (f *fakeAdmin) DropIndexedField(context.Context, store.IndexedField) error { return nil }

func (f *fakeAdmin) Purge(_ context.Context, p store.PurgeRequest) (store.PurgeJob, error) {
	if p.DryRun {
		return store.PurgeJob{RowsEstimate: 7}, nil
	}
	f.purges = append(f.purges, p)
	return store.PurgeJob{LightweightDeleted: true}, nil
}

type alwaysLeader struct{}

func (alwaysLeader) IsLeader(string) bool { return true }

// fakeQuerier answers every query with one row holding value.
type fakeQuerier struct{ value uint64 }

func (f *fakeQuerier) Run(context.Context, *tql.Compiled) (store.Result, error) {
	return store.Result{Kind: tql.KindTable, Rows: [][]any{{f.value}}}, nil
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }
