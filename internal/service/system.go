// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"time"

	"github.com/ZeraOrg/tidemark/internal/store"
)

// StorageInfo describes the ClickHouse server for /system.
type StorageInfo interface {
	Info(ctx context.Context) (store.ServerInfo, error)
}

// SystemReport is the body of GET /api/v1/system.
type SystemReport struct {
	Version       string             `json:"version"`
	StartedAt     time.Time          `json:"startedAt"`
	UptimeSeconds int64              `json:"uptimeSeconds"`
	QueueDepth    map[string]int     `json:"queueDepth"`
	Counters      map[string]float64 `json:"counters"`
	Streams       int                `json:"streams"`
	ClickHouse    *store.ServerInfo  `json:"clickhouse,omitempty"`
	ClickHouseErr string             `json:"clickhouseError,omitempty"`
}

// System assembles the system report.
type System struct {
	version  string
	started  time.Time
	now      func() time.Time
	depths   func() map[string]int
	counters func() map[string]float64
	streams  func() int
	storage  StorageInfo
}

// SystemDeps bundles the collaborators of System.
type SystemDeps struct {
	Version  string
	Started  time.Time
	Now      func() time.Time
	Depths   func() map[string]int
	Counters func() map[string]float64
	Streams  func() int
	Storage  StorageInfo
}

// NewSystem returns the system service.
func NewSystem(d SystemDeps) *System {
	return &System{version: d.Version, started: d.Started, now: d.Now, depths: d.Depths, counters: d.Counters,
		streams: d.Streams, storage: d.Storage}
}

// Report builds the report; storage errors are reported in the body, not as failures.
func (s *System) Report(ctx context.Context) SystemReport {
	r := SystemReport{Version: s.version, StartedAt: s.started, UptimeSeconds: int64(s.now().Sub(s.started).Seconds()),
		QueueDepth: s.depths(), Counters: s.counters(), Streams: s.streams()}
	st, err := s.storage.Info(ctx)
	if err != nil {
		r.ClickHouseErr = "unavailable"
		return r
	}
	r.ClickHouse = &st
	return r
}
