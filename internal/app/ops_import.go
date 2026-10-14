// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/cluster"
	"github.com/ZeraOrg/opentidemark/internal/importer"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/service"
)

// importProgressEvery is how often `tidemark import` prints the progress of a foreground job.
const importProgressEvery = 5 * time.Second

// CLIActor is the audit actor of jobs created and cancelled with `tidemark import` (SPEC §9.9).
var CLIActor = service.Actor{ID: "system:cli"}

// ErrImportFailed is returned when a foreground import ends failed or cancelled.
var ErrImportFailed = errors.New("import did not complete")

// ImportRequest is a `tidemark import <kind>` invocation.
type ImportRequest struct {
	Kind   string          // importer.Kind*
	Name   string          // job name ("" = default)
	Config json.RawMessage // elastic / seq configuration
	Stream string          // clef-file target stream
	Files  []string        // clef-file inputs, or the kibana export
	Detach bool            // queue the job for the server instead of running it here
}

// importEnv is an opened metadata store with the import service (and runner) on top.
type importEnv struct {
	db      *meta.DB
	svc     *importer.Service
	runner  *importer.Runner
	elector *cluster.Elector
	close   func()
}

// openImport opens what the import commands need; withRunner also connects ClickHouse.
func (o *Ops) openImport(ctx context.Context, withRunner bool) (*importEnv, error) {
	db, err := o.openMeta(ctx, true)
	if err != nil {
		return nil, err
	}
	env := &importEnv{db: db, close: func() { closeQuietly(db) }}
	if err := o.buildImportEnv(ctx, env, withRunner); err != nil {
		env.close()
		return nil, err
	}
	return env, nil
}

func (o *Ops) buildImportEnv(ctx context.Context, env *importEnv, withRunner bool) error {
	secret, err := ensureSecretKey(o.cfg.SecretKeyFile)
	if err != nil {
		return err
	}
	registry := service.NewStreamRegistry(meta.NewStreams(env.db), o.cfg.MaxStreams, o.logger)
	if err := registry.Load(ctx); err != nil {
		return err
	}
	// The CLI competes for job leases under its own holder id, so it never shares a lease with
	// the server of this node; jobs it creates are still pinned to the node's id (clef files).
	env.elector = cluster.NewElector(meta.NewLeases(env.db), auth.SystemClock{},
		cluster.ElectorConfig{NodeID: o.cfg.NodeID + "/cli-" + strconv.Itoa(os.Getpid())}, o.logger)
	d := importDeps{meta: env.db, database: o.cfg.ClickHouse.Database, writer: writerOptionsFor(o.cfg.ClickHouse),
		secret: secret, streams: registry, audit: service.NewAuditor(meta.NewAuditLog(env.db), o.logger, time.Now),
		elector: env.elector, nodeID: o.cfg.NodeID, dataDir: o.cfg.DataDir, logger: o.logger}
	if withRunner {
		conn, err := o.openWriter(ctx)
		if err != nil {
			return err
		}
		prev := env.close
		env.close = func() { closeQuietly(conn); prev() }
		d.conn = conn
	}
	svc, runner, err := buildImporter(d)
	env.svc, env.runner = svc, runner
	return err
}

// Import creates an import job and, unless detached, runs it in the foreground under its lease
// (or follows it while another node runs it) until it ends.
func (o *Ops) Import(ctx context.Context, req ImportRequest) (meta.ImportJob, error) {
	runHere := !req.Detach && req.Kind != importer.KindKibana
	env, err := o.openImport(ctx, runHere)
	if err != nil {
		return meta.ImportJob{}, err
	}
	defer env.close()
	job, err := createImport(ctx, env.svc, req)
	if err != nil {
		return meta.ImportJob{}, err
	}
	o.printf("import job %s (%s) %s\n", job.ID, job.Kind, job.Status)
	if !runHere {
		err = importOutcome(&job)
		return job, err
	}
	if job, err = o.follow(ctx, env, job.ID); err != nil {
		return job, err
	}
	err = importOutcome(&job)
	return job, err
}

func createImport(ctx context.Context, svc *importer.Service, req ImportRequest) (meta.ImportJob, error) {
	switch req.Kind {
	case importer.KindCLEFFile:
		return svc.CreateLocalFiles(ctx, CLIActor, req.Name, req.Stream, req.Files)
	case importer.KindKibana:
		if len(req.Files) != 1 {
			return meta.ImportJob{}, errors.New("exactly one Kibana export file is expected")
		}
		f, err := os.Open(filepath.Clean(req.Files[0]))
		if err != nil {
			return meta.ImportJob{}, err
		}
		defer closeQuietly(f)
		sent := false
		next := func() (importer.Upload, bool, error) {
			if sent {
				return importer.Upload{}, false, nil
			}
			sent = true
			return importer.Upload{Name: filepath.Base(req.Files[0]), Body: f}, true, nil
		}
		return svc.CreateWithUploads(ctx, CLIActor, importer.CreateInput{Kind: req.Kind, Name: req.Name,
			Config: json.RawMessage("{}")}, next)
	default:
		return svc.Create(ctx, CLIActor, importer.CreateInput{Kind: req.Kind, Name: req.Name, Config: req.Config})
	}
}

// follow runs the job when this process gets its lease and prints progress until it ends.
// Cancelling ctx (SIGINT) leaves the job resumable.
func (o *Ops) follow(ctx context.Context, env *importEnv, id string) (meta.ImportJob, error) {
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	name := cluster.LeaseImportPrefix + id
	env.elector.Register(name) // before Run, so its first round already competes for the job
	go env.elector.Run(rctx)
	ranHere := make(chan struct{}) // closed when the local run (if any) has ended
	go func() {
		defer close(ranHere)
		env.elector.Lead(rctx, name, func(lctx context.Context) {
			if err := env.runner.RunJob(lctx, id); err != nil && lctx.Err() == nil {
				o.logger.Error("import job failed", "id", id, "err", err)
			}
		})
	}()
	t := time.NewTicker(importProgressEvery)
	defer t.Stop()
	for {
		job, err := env.svc.Get(ctx, id)
		if err != nil {
			return meta.ImportJob{}, err
		}
		o.printProgress(&job)
		if importFinished(job.Status) {
			return job, nil
		}
		select {
		case <-ctx.Done():
			o.printf("interrupted; job %s stays resumable (it continues when the server or `tidemark import` picks it up)\n", id)
			return job, ctx.Err()
		case <-t.C:
		case <-ranHere:
			ranHere = nil // report the final state at once, then keep polling
		}
	}
}

func (o *Ops) printProgress(j *meta.ImportJob) {
	total := "?"
	if j.RowsTotal > 0 {
		total = strconv.FormatInt(j.RowsTotal, 10)
	}
	o.printf("%s  %-9s rows %d/%s  errors %d\n", time.Now().UTC().Format(time.TimeOnly), j.Status, j.RowsImported,
		total, j.Errors)
}

func importFinished(status string) bool {
	return status == meta.ImportCompleted || status == meta.ImportFailed || status == meta.ImportCancelled
}

// importOutcome turns a failed or cancelled job into an error (exit status 1).
func importOutcome(j *meta.ImportJob) error {
	if j.Status == meta.ImportFailed || j.Status == meta.ImportCancelled {
		if j.LastError != "" {
			return fmt.Errorf("%w: %s: %s", ErrImportFailed, j.Status, j.LastError)
		}
		return fmt.Errorf("%w: %s", ErrImportFailed, j.Status)
	}
	return nil
}

// ImportList prints the newest import jobs.
func (o *Ops) ImportList(ctx context.Context) error {
	env, err := o.openImport(ctx, false)
	if err != nil {
		return err
	}
	defer env.close()
	jobs, err := env.svc.List(ctx)
	if err != nil {
		return err
	}
	for i := range jobs {
		j := &jobs[i]
		o.printf("%s  %-9s %-9s rows %-10d errors %-6d %s\n", j.ID, j.Kind, j.Status, j.RowsImported, j.Errors, j.Name)
	}
	return nil
}

// ImportStatus prints one job as JSON (the API's ImportJob shape is not needed offline).
func (o *Ops) ImportStatus(ctx context.Context, id string) error {
	env, err := o.openImport(ctx, false)
	if err != nil {
		return err
	}
	defer env.close()
	j, err := env.svc.Get(ctx, id)
	if err != nil {
		return err
	}
	return o.writeJobJSON(&j)
}

// ImportCancel requests cancellation of a job.
func (o *Ops) ImportCancel(ctx context.Context, id string) error {
	env, err := o.openImport(ctx, false)
	if err != nil {
		return err
	}
	defer env.close()
	j, err := env.svc.Cancel(ctx, CLIActor, id)
	if err != nil {
		return err
	}
	o.printf("import job %s: cancellation requested (status %s)\n", j.ID, j.Status)
	return nil
}

func (o *Ops) writeJobJSON(j *meta.ImportJob) error {
	view := map[string]any{"id": j.ID, "kind": j.Kind, "name": j.Name, "status": j.Status,
		"config": importer.PublicConfig(j), "rowsImported": j.RowsImported, "rowsTotal": j.RowsTotal,
		"errors": j.Errors, "lastError": j.LastError, "warnings": nonNil(j.Warnings),
		"cancelRequested": j.CancelRequested, "requestedBy": j.RequestedBy, "startedAt": j.StartedAt,
		"finishedAt": j.FinishedAt, "createdAt": j.CreatedAt, "updatedAt": j.UpdatedAt}
	enc := json.NewEncoder(o.out)
	enc.SetIndent("", "  ")
	return enc.Encode(view)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
