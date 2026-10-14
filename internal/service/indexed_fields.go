// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
)

// Indexed field audit actions and limits (SPEC §12.4).
const (
	ActionIndexedFieldCreate = "indexed_field.create"
	ActionIndexedFieldDelete = "indexed_field.delete"
	MaxIndexedFields         = 50
	maxSlugLen               = 64
	maxSlugAttempts          = 100
)

// indexableKey restricts keys that may appear in DDL (SPEC §12.4).
var indexableKey = regexp.MustCompile(`^[A-Za-z0-9_.@:/\-]{1,128}$`)

// slugInvalid matches runs of characters not allowed in a column slug.
var slugInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// IndexedFieldRepo is the indexed_fields persistence.
type IndexedFieldRepo interface {
	Create(ctx context.Context, f *meta.IndexedField) error
	Get(ctx context.Context, id string) (meta.IndexedField, error)
	List(ctx context.Context) ([]meta.IndexedField, error)
	Delete(ctx context.Context, id string) error
}

// IndexedFieldLister exposes the indexed columns (for mappings and the TQL compiler).
type IndexedFieldLister interface {
	Columns(ctx context.Context) (map[model.Source]map[string]string, error)
}

// IndexStorage creates and drops materialized columns.
type IndexStorage interface {
	AddIndexedField(ctx context.Context, f store.IndexedField) error
	DropIndexedField(ctx context.Context, f store.IndexedField) error
}

// IndexedFields manages indexed fields (SPEC §12.4).
type IndexedFields struct {
	repo    IndexedFieldRepo
	storage IndexStorage
	audit   *Auditor
}

// NewIndexedFields returns the indexed field service.
func NewIndexedFields(repo IndexedFieldRepo, storage IndexStorage, audit *Auditor) *IndexedFields {
	return &IndexedFields{repo: repo, storage: storage, audit: audit}
}

// List returns all indexed fields.
func (s *IndexedFields) List(ctx context.Context) ([]meta.IndexedField, error) {
	return s.repo.List(ctx)
}

// Columns returns property key ⇒ column per source, the form of tql.Options.IndexedFields.
func (s *IndexedFields) Columns(ctx context.Context) (map[model.Source]map[string]string, error) {
	fields, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[model.Source]map[string]string{}
	for _, f := range fields {
		src := model.Source(f.Source)
		if out[src] == nil {
			out[src] = map[string]string{}
		}
		out[src][f.Key] = f.ColumnName
	}
	return out, nil
}

// Create indexes a property key: materialized column, bloom-filter skip index and
// MATERIALIZE INDEX for existing parts.
func (s *IndexedFields) Create(ctx context.Context, actor Actor, src model.Source, key string) (meta.IndexedField, error) {
	if !src.Valid() {
		return meta.IndexedField{}, invalid("source", "must be logs, spans or metrics")
	}
	if !indexableKey.MatchString(key) {
		return meta.IndexedField{}, invalid("key", "must be 1-128 characters of A-Z a-z 0-9 _ . @ : / -")
	}
	existing, err := s.repo.List(ctx)
	if err != nil {
		return meta.IndexedField{}, err
	}
	if len(existing) >= MaxIndexedFields {
		return meta.IndexedField{}, invalid("key", fmt.Sprintf("at most %d fields can be indexed", MaxIndexedFields))
	}
	used := map[string]bool{}
	for _, f := range existing {
		if f.Source == string(src) && f.Key == key {
			return meta.IndexedField{}, fmt.Errorf("%w: field is already indexed", meta.ErrConflict)
		}
		used[f.ColumnName] = true
	}
	column, err := ColumnFor(key, used)
	if err != nil {
		return meta.IndexedField{}, err
	}
	f := meta.IndexedField{Source: string(src), Key: key, ColumnName: column}
	if err := s.storage.AddIndexedField(ctx, store.IndexedField{Source: src, Key: key, Column: column}); err != nil {
		return meta.IndexedField{}, err
	}
	if err := s.repo.Create(ctx, &f); err != nil {
		return meta.IndexedField{}, errors.Join(err, s.storage.DropIndexedField(ctx, store.IndexedField{Source: src, Key: key, Column: column}))
	}
	s.audit.Record(ctx, actor, ActionIndexedFieldCreate, f.ID, map[string]any{"source": src, "key": key, "column": column})
	return f, nil
}

// Delete drops the index and the column.
func (s *IndexedFields) Delete(ctx context.Context, actor Actor, id string) error {
	f, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.storage.DropIndexedField(ctx, store.IndexedField{Source: model.Source(f.Source), Key: f.Key, Column: f.ColumnName}); err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	s.audit.Record(ctx, actor, ActionIndexedFieldDelete, id, map[string]any{"source": f.Source, "key": f.Key})
	return nil
}

// ColumnFor derives the column f_<slug> for a key: lower case, runs of other characters
// become "_", at most 64 characters; collisions get a numeric suffix.
func ColumnFor(key string, used map[string]bool) (string, error) {
	slug := strings.Trim(slugInvalid.ReplaceAllString(strings.ToLower(key), "_"), "_")
	if slug == "" {
		slug = "field"
	}
	if len(slug) > maxSlugLen {
		slug = slug[:maxSlugLen]
	}
	col := "f_" + slug
	for i := 2; used[col]; i++ {
		if i > maxSlugAttempts {
			return "", invalid("key", "no free column name")
		}
		suffix := "_" + strconv.Itoa(i)
		col = "f_" + slug[:min(len(slug), maxSlugLen-len(suffix))] + suffix
	}
	return col, nil
}
