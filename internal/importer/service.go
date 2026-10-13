// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/service"
)

// Service errors mapped by the API.
var (
	// ErrJobFinished is returned when cancelling a job that already ended.
	ErrJobFinished = errors.New("the import job has already finished")
	// ErrFileTooLarge is returned for an upload above its size limit.
	ErrFileTooLarge = errors.New("uploaded file is too large")
)

// Listing and naming limits.
const (
	MaxListJobs     = 100
	maxSignalName   = 128
	uploadDirPerm   = 0o700
	uploadFilePerm  = 0o600
	maxFileNameLen  = 255
	defaultJobLabel = "Import"
)

// reUnsafeName matches characters replaced in uploaded file names.
var reUnsafeName = regexp.MustCompile(`[^A-Za-z0-9._ -]`)

// Waker wakes the local runner after a job was queued.
type Waker interface{ Wake() }

// ServiceDeps are the collaborators of a Service.
type ServiceDeps struct {
	Jobs      JobStore
	Box       Sealer
	Signals   SignalSink
	Streams   StreamResolver
	Audit     *service.Auditor
	Runner    Waker
	UploadDir string // <data>/imports
	NodeID    string
	Now       func() time.Time
}

// Service creates, lists and cancels import jobs (SPEC §9.9, §11.5).
type Service struct{ d ServiceDeps }

// NewService returns the import job service.
func NewService(d ServiceDeps) *Service { return &Service{d: d} }

// CreateInput is a job request: kind, display name and kind-specific configuration.
type CreateInput struct {
	Kind   string
	Name   string
	Config json.RawMessage
}

// Upload is one uploaded file.
type Upload struct {
	Name string
	Body io.Reader
}

// NextUpload returns the next uploaded file; ok=false after the last one.
type NextUpload func() (u Upload, ok bool, err error)

// Get returns a job.
func (s *Service) Get(ctx context.Context, id string) (meta.ImportJob, error) {
	return s.d.Jobs.Get(ctx, id)
}

// List returns the newest jobs.
func (s *Service) List(ctx context.Context) ([]meta.ImportJob, error) {
	return s.d.Jobs.List(ctx, MaxListJobs)
}

// Cancel requests cancellation; a queued job is cancelled at once, a running one after its
// current page.
func (s *Service) Cancel(ctx context.Context, actor service.Actor, id string) (meta.ImportJob, error) {
	j, err := s.d.Jobs.Get(ctx, id)
	if err != nil {
		return meta.ImportJob{}, err
	}
	if j.Status != meta.ImportQueued && j.Status != meta.ImportRunning {
		return meta.ImportJob{}, ErrJobFinished
	}
	if err := s.d.Jobs.RequestCancel(ctx, id, s.d.Now()); err != nil {
		return meta.ImportJob{}, err
	}
	s.d.Audit.Record(ctx, actor, ActionImportCancel, id, nil)
	return s.d.Jobs.Get(ctx, id)
}

// Create queues an elastic or seq job (no files).
func (s *Service) Create(ctx context.Context, actor service.Actor, in CreateInput) (meta.ImportJob, error) {
	if err := validateName(&in); err != nil {
		return meta.ImportJob{}, err
	}
	var cfg any
	var err error
	switch in.Kind {
	case KindElastic:
		cfg, err = s.elasticConfig(in.Config)
	case KindSeq:
		cfg, err = s.seqConfig(in.Config)
	case KindCLEFFile, KindKibana:
		return meta.ImportJob{}, &service.ValidationError{Field: "kind", Msg: in.Kind + " imports need an uploaded file"}
	default:
		return meta.ImportJob{}, &service.ValidationError{Field: "kind", Msg: "must be elastic, seq, clef-file or kibana"}
	}
	if err != nil {
		return meta.ImportJob{}, err
	}
	return s.queue(ctx, actor, &meta.ImportJob{ID: meta.NewID(), Kind: in.Kind, Name: in.Name}, cfg)
}

func (s *Service) elasticConfig(raw json.RawMessage) (*ElasticConfig, error) {
	var c ElasticConfig
	if err := decodeStrict(raw, &c); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	if c.To == nil { // freeze the range so a resumed job re-reads identical pages
		now := s.d.Now().UTC()
		c.To = &now
	}
	return &c, c.Auth.seal(s.d.Box)
}

func (s *Service) seqConfig(raw json.RawMessage) (*SeqConfig, error) {
	var c SeqConfig
	if err := decodeStrict(raw, &c); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	if c.To == nil {
		now := s.d.Now().UTC()
		c.To = &now
	}
	return &c, sealSecret(s.d.Box, &c.APIKey)
}

func (s *Service) queue(ctx context.Context, actor service.Actor, j *meta.ImportJob, cfg any) (meta.ImportJob, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return meta.ImportJob{}, fmt.Errorf("encode import config: %w", err)
	}
	j.Config, j.RequestedBy, j.Status = raw, actor.ID, meta.ImportQueued
	if err := s.d.Jobs.Create(ctx, j); err != nil {
		return meta.ImportJob{}, err
	}
	s.d.Audit.Record(ctx, actor, ActionImportCreate, j.ID, map[string]any{"kind": j.Kind, "name": j.Name})
	if s.d.Runner != nil {
		s.d.Runner.Wake()
	}
	return s.d.Jobs.Get(ctx, j.ID)
}

func validateName(in *CreateInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = defaultJobLabel + " " + in.Kind
	}
	if utf8.RuneCountInString(in.Name) > MaxNameLen {
		return &service.ValidationError{Field: "name", Msg: "must be at most 128 characters"}
	}
	return nil
}

// CreateWithUploads handles clef-file jobs (files stored under the upload directory, job pinned
// to this node) and kibana exports (processed at once; the job is returned finished).
func (s *Service) CreateWithUploads(ctx context.Context, actor service.Actor, in CreateInput, next NextUpload) (meta.ImportJob, error) {
	if err := validateName(&in); err != nil {
		return meta.ImportJob{}, err
	}
	switch in.Kind {
	case KindCLEFFile:
		return s.createCLEF(ctx, actor, in, next)
	case KindKibana:
		return s.createKibana(ctx, actor, in, next)
	default:
		return meta.ImportJob{}, &service.ValidationError{Field: "kind", Msg: "only clef-file and kibana imports take files"}
	}
}

func (s *Service) createCLEF(ctx context.Context, actor service.Actor, in CreateInput, next NextUpload) (meta.ImportJob, error) {
	var c CLEFConfig
	if err := decodeStrict(in.Config, &c); err != nil {
		return meta.ImportJob{}, err
	}
	if len(c.Files) > 0 || c.Node != "" {
		return meta.ImportJob{}, invalid("files", "files are uploaded, not configured")
	}
	id := meta.NewID()
	dir := filepath.Join(s.d.UploadDir, id)
	files, err := s.saveUploads(dir, next)
	if err != nil {
		_ = os.RemoveAll(dir) // nothing references the partial upload
		return meta.ImportJob{}, err
	}
	c.Files, c.Node = files, s.d.NodeID
	if err := c.validate(); err != nil {
		_ = os.RemoveAll(dir) // nothing references the rejected upload
		return meta.ImportJob{}, err
	}
	return s.queue(ctx, actor, &meta.ImportJob{ID: id, Kind: KindCLEFFile, Name: in.Name}, &c)
}

// CreateLocalFiles queues a clef-file job over files already on this node (CLI).
func (s *Service) CreateLocalFiles(ctx context.Context, actor service.Actor, name, stream string, paths []string) (meta.ImportJob, error) {
	in := CreateInput{Kind: KindCLEFFile, Name: name}
	if err := validateName(&in); err != nil {
		return meta.ImportJob{}, err
	}
	c := CLEFConfig{Stream: stream, Node: s.d.NodeID}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return meta.ImportJob{}, fmt.Errorf("file %s: %w", p, err)
		}
		st, err := os.Stat(abs)
		if err != nil || !st.Mode().IsRegular() {
			return meta.ImportJob{}, &service.ValidationError{Field: "files", Msg: p + " is not a readable file"}
		}
		c.Files = append(c.Files, FileRef{Name: filepath.Base(abs), Path: abs, Size: st.Size()})
	}
	if err := c.validate(); err != nil {
		return meta.ImportJob{}, err
	}
	return s.queue(ctx, actor, &meta.ImportJob{ID: meta.NewID(), Kind: KindCLEFFile, Name: in.Name}, &c)
}

// saveUploads streams every upload to dir (each at most MaxUploadFileBytes).
func (s *Service) saveUploads(dir string, next NextUpload) ([]FileRef, error) {
	if err := os.MkdirAll(dir, uploadDirPerm); err != nil {
		return nil, fmt.Errorf("create upload directory: %w", err)
	}
	var files []FileRef
	for {
		u, ok, err := next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return files, nil
		}
		if len(files) >= MaxUploadFiles {
			return nil, invalid("files", fmt.Sprintf("at most %d files", MaxUploadFiles))
		}
		path := filepath.Join(dir, fmt.Sprintf("%02d.ndjson", len(files)))
		n, err := saveFile(path, u.Body, MaxUploadFileBytes)
		if err != nil {
			return nil, err
		}
		files = append(files, FileRef{Name: displayName(u.Name), Path: path, Size: n})
	}
}

func saveFile(path string, body io.Reader, limit int64) (int64, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, uploadFilePerm)
	if err != nil {
		return 0, fmt.Errorf("store upload: %w", err)
	}
	n, err := io.Copy(f, io.LimitReader(body, limit+1))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return 0, fmt.Errorf("store upload: %w", err)
	case n > limit:
		return 0, ErrFileTooLarge
	}
	return n, nil
}

func displayName(name string) string {
	name = reUnsafeName.ReplaceAllString(filepath.Base(strings.ReplaceAll(name, `\`, "/")), "_")
	if len(name) > maxFileNameLen {
		name = name[:maxFileNameLen]
	}
	if name == "" || name == "." || name == "/" {
		return "upload"
	}
	return name
}

// createKibana imports a saved-objects export synchronously and records it as a finished job.
func (s *Service) createKibana(ctx context.Context, actor service.Actor, in CreateInput, next NextUpload) (meta.ImportJob, error) {
	if err := decodeStrict(in.Config, &struct{}{}); err != nil {
		return meta.ImportJob{}, err
	}
	u, ok, err := next()
	switch {
	case err != nil:
		return meta.ImportJob{}, err
	case !ok:
		return meta.ImportJob{}, invalid("file", "a Kibana .ndjson export is required")
	}
	body, err := io.ReadAll(io.LimitReader(u.Body, MaxKibanaExportBytes+1))
	if err != nil {
		return meta.ImportJob{}, fmt.Errorf("read upload: %w", err)
	}
	if len(body) > MaxKibanaExportBytes {
		return meta.ImportJob{}, ErrFileTooLarge
	}
	if _, more, err := next(); err != nil || more {
		return meta.ImportJob{}, invalid("file", "exactly one export file is expected")
	}
	cfg := KibanaConfig{File: FileRef{Name: displayName(u.Name), Size: int64(len(body))}}
	job, err := s.queue(ctx, actor, &meta.ImportJob{ID: meta.NewID(), Kind: KindKibana, Name: in.Name}, &cfg)
	if err != nil {
		return meta.ImportJob{}, err
	}
	return s.runKibana(ctx, actor, job, body)
}

func (s *Service) runKibana(ctx context.Context, actor service.Actor, job meta.ImportJob, body []byte) (meta.ImportJob, error) {
	now := s.d.Now()
	if err := s.d.Jobs.MarkRunning(ctx, job.ID, now); err != nil {
		return meta.ImportJob{}, err
	}
	res, runErr := ImportKibana(ctx, strings.NewReader(string(body)), s.d.Signals, s.d.Streams, jobOwner(&job))
	status, lastErr := meta.ImportCompleted, ""
	if runErr != nil {
		status, lastErr = meta.ImportFailed, runErr.Error()
	}
	p := meta.ImportProgress{Checkpoint: json.RawMessage("{}"), RowsImported: int64(res.Signals + res.Streams),
		RowsTotal: int64(res.Signals + res.Streams), Errors: int64(res.SignalsDisabled)}
	if err := s.d.Jobs.SaveProgress(ctx, job.ID, p); err != nil {
		return meta.ImportJob{}, err
	}
	if err := s.d.Jobs.SetWarnings(ctx, job.ID, capWarnings(res.Warnings)); err != nil {
		return meta.ImportJob{}, err
	}
	if err := s.d.Jobs.Finish(ctx, job.ID, status, lastErr, s.d.Now()); err != nil {
		return meta.ImportJob{}, err
	}
	s.d.Audit.Record(ctx, actor, ActionImportDone, job.ID, map[string]any{"status": status, "signals": res.Signals,
		"signalsDisabled": res.SignalsDisabled, "streams": res.Streams})
	return s.d.Jobs.Get(ctx, job.ID)
}

func capWarnings(w []string) []string {
	if len(w) <= maxWarnings {
		return w
	}
	return append(append([]string(nil), w[:maxWarnings]...), "further warnings omitted")
}

// PublicConfig returns the job configuration without secrets or server-side paths, for the API.
func PublicConfig(j *meta.ImportJob) json.RawMessage {
	var out any
	switch j.Kind {
	case KindElastic:
		var c ElasticConfig
		_ = json.Unmarshal(j.Config, &c) // written by this package; a broken row shows defaults
		c.Auth.Password, c.Auth.APIKey, c.Auth.Token = "", "", ""
		out = c
	case KindSeq:
		var c SeqConfig
		_ = json.Unmarshal(j.Config, &c) // as above
		c.APIKey = ""
		out = c
	case KindCLEFFile:
		var c CLEFConfig
		_ = json.Unmarshal(j.Config, &c) // as above
		for i := range c.Files {
			c.Files[i].Path = ""
		}
		out = c
	case KindKibana:
		var c KibanaConfig
		_ = json.Unmarshal(j.Config, &c) // as above
		out = c
	}
	raw, err := json.Marshal(out)
	if err != nil || out == nil {
		return json.RawMessage("{}")
	}
	return raw
}

// SignalStore persists imported signals (meta.Signals).
type SignalStore interface {
	ListVisible(ctx context.Context, userID string) ([]meta.Signal, error)
	CreateImported(ctx context.Context, s *meta.Signal, disabled bool) error
}

// Signals implements SignalSink on the signals repository: imported signals are shared,
// owned by the requesting admin, and a signal of the same owner and name is not created twice
// (a resumed job may import signals again).
type Signals struct{ Store SignalStore }

// ImportSignal implements SignalSink.
func (s Signals) ImportSignal(ctx context.Context, owner, name, tqlText string, enabled bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Imported signal"
	}
	if utf8.RuneCountInString(name) > maxSignalName {
		name = string([]rune(name)[:maxSignalName])
	}
	visible, err := s.Store.ListVisible(ctx, owner)
	if err != nil {
		return err
	}
	for _, v := range visible {
		if v.OwnerID == owner && v.Name == name {
			return nil
		}
	}
	return s.Store.CreateImported(ctx, &meta.Signal{Name: name, TQL: tqlText, OwnerID: owner, Shared: true,
		Source: "logs"}, !enabled)
}
