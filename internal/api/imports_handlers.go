// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/netip"
	"time"

	"github.com/ZeraOrg/tidemark/internal/importer"
	"github.com/ZeraOrg/tidemark/internal/meta"
)

// maxUploadRequest bounds a multipart import request: every file at its limit plus the job part.
const maxUploadRequest = importer.MaxUploadFiles*importer.MaxUploadFileBytes + MaxRequestBody

// ImportsAPI serves /imports (SPEC §9.9, §11.5).
type ImportsAPI struct {
	Imports  *importer.Service
	ClientIP func(*http.Request) netip.Addr
}

// Routes implements Registrar.
func (a *ImportsAPI) Routes() []Route {
	return []Route{
		{http.MethodGet, "/imports", Admin, a.list},
		{http.MethodPost, "/imports", Admin, a.create},
		{http.MethodGet, "/imports/{id}", Admin, a.get},
		{http.MethodPost, "/imports/{id}/cancel", Admin, a.cancel},
	}
}

// BodyLimits implements BodyLimiter: POST /imports takes multipart uploads.
func (a *ImportsAPI) BodyLimits() map[string]int64 {
	return map[string]int64{"POST /imports": maxUploadRequest}
}

// importJobView is the ImportJob response shape (SPEC §11.5).
type importJobView struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"`
	Name            string          `json:"name"`
	Status          string          `json:"status"`
	Config          json.RawMessage `json:"config"`
	RowsImported    int64           `json:"rowsImported"`
	RowsTotal       int64           `json:"rowsTotal"`
	Errors          int64           `json:"errors"`
	LastError       string          `json:"lastError"`
	Warnings        []string        `json:"warnings"`
	CancelRequested bool            `json:"cancelRequested"`
	RequestedBy     string          `json:"requestedBy"`
	StartedAt       *time.Time      `json:"startedAt"`
	FinishedAt      *time.Time      `json:"finishedAt"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

func toImportView(j *meta.ImportJob) importJobView {
	v := importJobView{ID: j.ID, Kind: j.Kind, Name: j.Name, Status: j.Status, Config: importer.PublicConfig(j),
		RowsImported: j.RowsImported, RowsTotal: j.RowsTotal, Errors: j.Errors, LastError: j.LastError,
		Warnings: j.Warnings, CancelRequested: j.CancelRequested, RequestedBy: j.RequestedBy, StartedAt: j.StartedAt,
		FinishedAt: j.FinishedAt, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt}
	if v.Warnings == nil {
		v.Warnings = []string{}
	}
	return v
}

func (a *ImportsAPI) list(w http.ResponseWriter, r *http.Request) error {
	jobs, err := a.Imports.List(r.Context())
	if err != nil {
		return err
	}
	out := make([]importJobView, 0, len(jobs))
	for i := range jobs {
		out = append(out, toImportView(&jobs[i]))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *ImportsAPI) get(w http.ResponseWriter, r *http.Request) error {
	j, err := a.Imports.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toImportView(&j))
	return nil
}

func (a *ImportsAPI) cancel(w http.ResponseWriter, r *http.Request) error {
	j, err := a.Imports.Cancel(r.Context(), actor(r, a.ClientIP), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toImportView(&j))
	return nil
}

// importRequest is the job description: the JSON body, or the "job" part of a multipart upload.
type importRequest struct {
	Kind   string          `json:"kind"`
	Name   string          `json:"name"`
	Config json.RawMessage `json:"config"`
}

func (in importRequest) input() importer.CreateInput {
	return importer.CreateInput{Kind: in.Kind, Name: in.Name, Config: in.Config}
}

func (a *ImportsAPI) create(w http.ResponseWriter, r *http.Request) error {
	mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var job meta.ImportJob
	var err error
	if mediaType == "multipart/form-data" {
		job, err = a.createUpload(r, params["boundary"])
	} else {
		var in importRequest
		if err := decode(r, &in); err != nil {
			return err
		}
		job, err = a.Imports.Create(r.Context(), actor(r, a.ClientIP), in.input())
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toImportView(&job))
	return nil
}

// createUpload reads the "job" part first, then streams every "file" part to the service.
func (a *ImportsAPI) createUpload(r *http.Request, boundary string) (meta.ImportJob, error) {
	if boundary == "" {
		return meta.ImportJob{}, errBadRequest("multipart boundary missing")
	}
	mr := multipart.NewReader(r.Body, boundary)
	first, err := mr.NextPart()
	if err != nil || first.FormName() != "job" {
		return meta.ImportJob{}, errBadRequest(`the first part must be "job" (JSON)`)
	}
	var in importRequest
	limited := http.Request{Body: io.NopCloser(io.LimitReader(first, MaxRequestBody))}
	if err := decode(&limited, &in); err != nil {
		return meta.ImportJob{}, err
	}
	next := func() (importer.Upload, bool, error) {
		p, err := mr.NextPart()
		switch {
		case errors.Is(err, io.EOF):
			return importer.Upload{}, false, nil
		case err != nil:
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return importer.Upload{}, false, importer.ErrFileTooLarge
			}
			return importer.Upload{}, false, errBadRequest("invalid multipart body")
		case p.FormName() != "file":
			return importer.Upload{}, false, errBadRequest(`unexpected part %q (only "file" parts follow "job")`, p.FormName())
		}
		return importer.Upload{Name: p.FileName(), Body: p}, true, nil
	}
	return a.Imports.CreateWithUploads(r.Context(), actor(r, a.ClientIP), in.input(), next)
}
