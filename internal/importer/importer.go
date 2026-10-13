// SPDX-License-Identifier: Apache-2.0

// Package importer moves existing data into Tidemark (SPEC §9.9): Elasticsearch/OpenSearch
// indices, Seq servers (events and signals), Kibana saved-object exports and CLEF/NDJSON files.
// Imports run as resumable jobs stored in import_jobs; each page of events is written with a
// deterministic deduplication token derived from the job and its checkpoint, so a resumed job
// never stores a page twice.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ZeraOrg/tidemark/internal/model"
)

// Job kinds (import_jobs.kind).
const (
	KindElastic  = "elastic"
	KindSeq      = "seq"
	KindCLEFFile = "clef-file"
	KindKibana   = "kibana"
)

// Limits and defaults (SPEC §9.9, §11.5).
const (
	DefaultElasticPageSize = 5000
	MaxElasticPageSize     = 10000
	DefaultSeqPageSize     = 1000
	MaxSeqPageSize         = 10000
	MaxUploadFileBytes     = 1 << 30 // per uploaded CLEF/NDJSON file
	MaxUploadFiles         = 16
	MaxKibanaExportBytes   = 64 << 20
	MaxNameLen             = 128
	MaxURLLen              = 2048
	MaxQueryBytes          = 64 << 10
	MaxFilterLen           = 8192
	DefaultTimeField       = "@timestamp"
	sourceRequestTimeout   = 2 * time.Minute
)

// Errors returned by sources and the job runner.
var (
	// ErrPermanent marks a source error that retrying cannot fix (4xx, malformed response).
	ErrPermanent = errors.New("permanent source error")
	// ErrNotRunnable is returned when a job is already finished.
	ErrNotRunnable = errors.New("import job is not running or queued")
)

// Page is one unit of work: the events read after a checkpoint and the checkpoint that
// follows them. A page is written and committed atomically from the job's point of view.
type Page struct {
	Events     []model.Event
	Checkpoint json.RawMessage // position after this page
	Key        string          // identity of the page content (part of the insert token)
	Done       bool            // no more data after this page
	Total      int64           // total rows reported by the source (0 = unknown)
	Skipped    int64           // records that could not be decoded
	LastError  string          // reason for the last skipped record
}

// Source reads pages of events for one job.
type Source interface {
	// Next returns the page following cp (empty or "{}" = the beginning).
	Next(ctx context.Context, cp json.RawMessage) (Page, error)
	// Close releases open files; final (the job ended) also releases server-side resources
	// such as a point in time, which an interrupted job keeps for its resumption.
	Close(ctx context.Context, final bool) error
}

// Auth is the authentication of an HTTP source.
type Auth struct {
	Type     string `json:"type"` // none | basic | apiKey | bearer
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"` // secret
	APIKey   string `json:"apiKey,omitempty"`   // secret
	Token    string `json:"token,omitempty"`    // secret
}

// Auth types.
const (
	AuthNone   = "none"
	AuthBasic  = "basic"
	AuthAPIKey = "apiKey"
	AuthBearer = "bearer"
)

// ElasticConfig configures an Elasticsearch/OpenSearch import.
type ElasticConfig struct {
	URL       string          `json:"url"`
	Auth      Auth            `json:"auth"`
	CACert    string          `json:"caCert,omitempty"` // PEM bundle trusted in addition to the system roots
	Index     string          `json:"index"`            // index name or pattern
	TimeField string          `json:"timeField,omitempty"`
	From      *time.Time      `json:"from,omitempty"`
	To        *time.Time      `json:"to,omitempty"`
	Query     json.RawMessage `json:"query,omitempty"` // ES query DSL object, ANDed with the time range
	Stream    string          `json:"stream,omitempty"`
	PageSize  int             `json:"pageSize,omitempty"`
}

// SeqConfig configures a Seq import.
type SeqConfig struct {
	URL           string     `json:"url"`
	APIKey        string     `json:"apiKey,omitempty"` // secret
	From          *time.Time `json:"from,omitempty"`
	To            *time.Time `json:"to,omitempty"`
	Filter        string     `json:"filter,omitempty"` // strict Seq filter expression
	Stream        string     `json:"stream,omitempty"`
	PageSize      int        `json:"pageSize,omitempty"`
	ImportEvents  *bool      `json:"importEvents,omitempty"` // default true
	ImportSignals bool       `json:"importSignals,omitempty"`
}

// FileRef is an uploaded or local file of a clef-file job.
type FileRef struct {
	Name string `json:"name"`           // display name
	Path string `json:"path,omitempty"` // server-side path, never returned by the API
	Size int64  `json:"size"`
}

// CLEFConfig configures a CLEF/NDJSON file import. Files exist on one node only, so the job is
// pinned to Node.
type CLEFConfig struct {
	Stream string    `json:"stream,omitempty"`
	Files  []FileRef `json:"files"`
	Node   string    `json:"node"`
}

// KibanaConfig configures a Kibana saved-objects import (processed when uploaded).
type KibanaConfig struct {
	File FileRef `json:"file"`
}

// events reports whether a Seq job imports events (default true).
func (c *SeqConfig) events() bool { return c.ImportEvents == nil || *c.ImportEvents }
