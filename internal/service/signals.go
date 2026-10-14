// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// SignalStore is the signals persistence used by Signals.
type SignalStore interface {
	Create(ctx context.Context, s *meta.Signal) error
	Get(ctx context.Context, id string) (meta.Signal, error)
	ListVisible(ctx context.Context, userID string) ([]meta.Signal, error)
	UpdateIfUnchanged(ctx context.Context, s *meta.Signal, expected time.Time) error
	Delete(ctx context.Context, id string) error
}

// Signal limits.
const (
	maxSignalName    = 128
	maxSignalsOwned  = 500
	maxSignalTQLSize = tql.MaxQueryBytes
)

// reSignalColor accepts design-token names and hex colours.
var reSignalColor = regexp.MustCompile(`^[A-Za-z0-9#-]{0,32}$`)

// Signals manages saved TQL filters (SPEC §3, §11.4). Everyone sees their own and shared
// signals; only the owner or an admin may change or delete one.
type Signals struct {
	store SignalStore
}

// NewSignals returns the signals service.
func NewSignals(store SignalStore) *Signals { return &Signals{store: store} }

// SignalInput is the writable part of a signal.
type SignalInput struct {
	Name   string
	TQL    string
	Color  string
	Shared bool
	Source string // logs (default), spans or metrics
}

// signalVisible: shared signals and one's own are visible; admins may open any signal by id.
func signalVisible(s meta.Signal, p auth.Principal) bool {
	return s.Shared || s.OwnerID == p.UserID || p.Role.Allows(auth.RoleAdmin)
}

func signalWritable(s meta.Signal, p auth.Principal) bool {
	return s.OwnerID == p.UserID || p.Role.Allows(auth.RoleAdmin)
}

// List returns the signals visible to p.
func (s *Signals) List(ctx context.Context, p auth.Principal) ([]meta.Signal, error) {
	return s.store.ListVisible(ctx, p.UserID)
}

// Get returns a visible signal.
func (s *Signals) Get(ctx context.Context, p auth.Principal, id string) (meta.Signal, error) {
	sig, err := s.store.Get(ctx, id)
	if err != nil {
		return meta.Signal{}, err
	}
	if !signalVisible(sig, p) {
		return meta.Signal{}, meta.ErrNotFound
	}
	return sig, nil
}

// validate normalizes and checks the input; the TQL must be a filter for the source.
func (in *SignalInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > maxSignalName {
		return invalid("name", "must be 1-128 characters")
	}
	if in.Source == "" {
		in.Source = string(model.SourceLogs)
	}
	src := model.Source(in.Source)
	if !src.Valid() {
		return invalid("source", "must be logs, spans or metrics")
	}
	if !reSignalColor.MatchString(in.Color) {
		return invalid("color", "must be a colour token or hex value")
	}
	if strings.TrimSpace(in.TQL) == "" || len(in.TQL) > maxSignalTQLSize {
		return invalid("tql", "must be a non-empty query")
	}
	q, err := parseFor(src, in.TQL)
	if err != nil {
		return err
	}
	if len(q.Stages) > 0 {
		return invalid("tql", "a signal is a filter; pipe stages are not allowed")
	}
	return tql.Check(q)
}

// Create stores a new signal owned by p.
func (s *Signals) Create(ctx context.Context, p auth.Principal, in SignalInput) (meta.Signal, error) {
	if err := in.validate(); err != nil {
		return meta.Signal{}, err
	}
	visible, err := s.store.ListVisible(ctx, p.UserID)
	if err != nil {
		return meta.Signal{}, err
	}
	owned := 0
	for _, v := range visible {
		if v.OwnerID == p.UserID {
			owned++
		}
	}
	if owned >= maxSignalsOwned {
		return meta.Signal{}, invalid("name", "signal limit reached")
	}
	sig := meta.Signal{Name: in.Name, TQL: in.TQL, Color: in.Color, OwnerID: p.UserID, Shared: in.Shared, Source: in.Source}
	if err := s.store.Create(ctx, &sig); err != nil {
		return meta.Signal{}, err
	}
	return sig, nil
}

// Update replaces a signal if it is unchanged since expected (its updatedAt).
func (s *Signals) Update(ctx context.Context, p auth.Principal, id string, in SignalInput, expected time.Time) (meta.Signal, error) {
	if expected.IsZero() {
		return meta.Signal{}, invalid("updatedAt", "is required (optimistic concurrency)")
	}
	if err := in.validate(); err != nil {
		return meta.Signal{}, err
	}
	sig, err := s.writable(ctx, p, id)
	if err != nil {
		return meta.Signal{}, err
	}
	sig.Name, sig.TQL, sig.Color, sig.Shared, sig.Source = in.Name, in.TQL, in.Color, in.Shared, in.Source
	if err := s.store.UpdateIfUnchanged(ctx, &sig, expected); err != nil {
		return meta.Signal{}, err
	}
	return sig, nil
}

// Delete removes a signal.
func (s *Signals) Delete(ctx context.Context, p auth.Principal, id string) error {
	if _, err := s.writable(ctx, p, id); err != nil {
		return err
	}
	return s.store.Delete(ctx, id)
}

func (s *Signals) writable(ctx context.Context, p auth.Principal, id string) (meta.Signal, error) {
	sig, err := s.Get(ctx, p, id)
	if err != nil {
		return meta.Signal{}, err
	}
	if !signalWritable(sig, p) {
		return meta.Signal{}, ErrForbidden
	}
	return sig, nil
}
