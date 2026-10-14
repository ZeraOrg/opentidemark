// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/app"
	"github.com/ZeraOrg/opentidemark/internal/importer"
)

// maxCLIFileBytes bounds the CA bundle and query files read by `tidemark import elastic`.
const maxCLIFileBytes = 1 << 20

const importUsage = `usage:
  tidemark import clef <files…> [--stream x] [--name n] [--detach]
  tidemark import elastic --url U --index I [--auth-type none|basic|apiKey|bearer] [--username u]
      [--password-env VAR] [--api-key-env VAR] [--token-env VAR] [--ca-file f] [--time-field f]
      [--from t] [--to t] [--query-file f] [--stream x] [--page-size n] [--name n] [--detach]
  tidemark import seq --url U [--api-key-env VAR] [--from t] [--to t] [--filter f] [--signals]
      [--no-events] [--stream x] [--page-size n] [--name n] [--detach]
  tidemark import kibana <export.ndjson> [--name n]
  tidemark import list | status <id> | cancel <id>`

// importCommand implements "import <kind|list|status|cancel> …" (SPEC §9.9).
func importCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(importUsage)
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "list":
		return withOps(args, stdout, stderr, func(ctx context.Context, o *app.Ops) error { return o.ImportList(ctx) })
	case "status", "cancel":
		return importJobCommand(sub, args, stdout, stderr)
	case "clef", "elastic", "seq", "kibana":
		req, rest, err := parseImport(sub, args, os.Getenv)
		if err != nil {
			return err
		}
		return withOps(rest, stdout, stderr, func(ctx context.Context, o *app.Ops) error {
			_, err := o.Import(ctx, req)
			return err
		})
	default:
		return fmt.Errorf("unknown import command %q\n%s", sub, importUsage)
	}
}

func importJobCommand(sub string, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: tidemark import %s <job id>", sub)
	}
	id, rest := args[0], args[1:]
	return withOps(rest, stdout, stderr, func(ctx context.Context, o *app.Ops) error {
		if sub == "status" {
			return o.ImportStatus(ctx, id)
		}
		return o.ImportCancel(ctx, id)
	})
}

// importFlags are the flags of every import kind (name ⇒ takes a value).
var importFlags = cmdFlags{"stream": true, "name": true, "detach": false, "url": true, "index": true,
	"auth-type": true, "username": true, "password-env": true, "api-key-env": true, "token-env": true,
	"ca-file": true, "time-field": true, "from": true, "to": true, "query-file": true, "page-size": true,
	"filter": true, "signals": false, "no-events": false}

// kindFlags lists the flags each kind accepts besides name and detach.
var kindFlags = map[string][]string{
	"clef": {"stream"},
	"elastic": {"stream", "url", "index", "auth-type", "username", "password-env", "api-key-env", "token-env",
		"ca-file", "time-field", "from", "to", "query-file", "page-size"},
	"seq":    {"stream", "url", "api-key-env", "from", "to", "filter", "signals", "no-events", "page-size"},
	"kibana": {},
}

// parseImport turns the arguments of `import <kind>` into a request; positional arguments that
// are not flags are files (clef, kibana); remaining --flags go to the configuration loader.
// Secrets are read from the environment variables named by the *-env flags only.
func parseImport(kind string, args []string, getenv func(string) string) (app.ImportRequest, []string, error) {
	own, rest, err := importFlags.split(args)
	if err != nil {
		return app.ImportRequest{}, nil, err
	}
	if err := checkKindFlags(kind, own); err != nil {
		return app.ImportRequest{}, nil, err
	}
	var files, cfgArgs []string
	for i := 0; i < len(rest); i++ {
		switch {
		case !strings.HasPrefix(rest[i], "-"):
			files = append(files, rest[i])
		case !strings.Contains(rest[i], "=") && i+1 < len(rest) && !strings.HasPrefix(rest[i+1], "-"):
			cfgArgs = append(cfgArgs, rest[i], rest[i+1]) // configuration flag with a separate value
			i++
		default:
			cfgArgs = append(cfgArgs, rest[i])
		}
	}
	req := app.ImportRequest{Name: own["name"], Detach: own["detach"] == "true", Stream: own["stream"], Files: files}
	switch kind {
	case "clef":
		req.Kind = importer.KindCLEFFile
		if len(files) == 0 {
			return req, nil, errors.New("usage: tidemark import clef <files…> [--stream x]")
		}
	case "kibana":
		req.Kind = importer.KindKibana
		if len(files) != 1 {
			return req, nil, errors.New("usage: tidemark import kibana <export.ndjson>")
		}
	case "elastic":
		req.Kind = importer.KindElastic
		req.Config, err = elasticConfig(own, getenv)
	case "seq":
		req.Kind = importer.KindSeq
		req.Config, err = seqConfig(own, getenv)
	}
	if err == nil && (kind == "elastic" || kind == "seq") && len(files) > 0 {
		err = fmt.Errorf("unexpected argument %q", files[0])
	}
	return req, cfgArgs, err
}

func checkKindFlags(kind string, own map[string]string) error {
	allowed := map[string]bool{"name": true, "detach": kind != "kibana"}
	for _, f := range kindFlags[kind] {
		allowed[f] = true
	}
	for f := range own {
		if !allowed[f] {
			return fmt.Errorf("--%s is not supported by `import %s`", f, kind)
		}
	}
	return nil
}

func elasticConfig(own map[string]string, getenv func(string) string) (json.RawMessage, error) {
	c := importer.ElasticConfig{URL: own["url"], Index: own["index"], TimeField: own["time-field"], Stream: own["stream"],
		Auth: importer.Auth{Type: own["auth-type"], Username: own["username"]}}
	var err error
	if c.Auth.Password, err = secretEnv(own, "password-env", getenv); err != nil {
		return nil, err
	}
	if c.Auth.APIKey, err = secretEnv(own, "api-key-env", getenv); err != nil {
		return nil, err
	}
	if c.Auth.Token, err = secretEnv(own, "token-env", getenv); err != nil {
		return nil, err
	}
	if c.From, c.To, err = timeRange(own); err != nil {
		return nil, err
	}
	if c.PageSize, err = pageSize(own); err != nil {
		return nil, err
	}
	if own["ca-file"] != "" {
		ca, err := readSmallFile(own["ca-file"])
		if err != nil {
			return nil, err
		}
		c.CACert = string(ca)
	}
	if own["query-file"] != "" {
		if c.Query, err = readSmallFile(own["query-file"]); err != nil {
			return nil, err
		}
	}
	return json.Marshal(c)
}

func seqConfig(own map[string]string, getenv func(string) string) (json.RawMessage, error) {
	c := importer.SeqConfig{URL: own["url"], Filter: own["filter"], Stream: own["stream"],
		ImportSignals: own["signals"] == "true"}
	if own["no-events"] == "true" {
		no := false
		c.ImportEvents = &no
	}
	var err error
	if c.APIKey, err = secretEnv(own, "api-key-env", getenv); err != nil {
		return nil, err
	}
	if c.From, c.To, err = timeRange(own); err != nil {
		return nil, err
	}
	if c.PageSize, err = pageSize(own); err != nil {
		return nil, err
	}
	return json.Marshal(c) //nolint:gosec // handed to importer.Service, which seals the key before storing it
}

// secretEnv reads the secret from the environment variable named by flag (never from a flag
// value, which would show up in process listings and shell history).
func secretEnv(own map[string]string, flag string, getenv func(string) string) (string, error) {
	name := own[flag]
	if name == "" {
		return "", nil
	}
	v := getenv(name)
	if v == "" {
		return "", fmt.Errorf("--%s: environment variable %s is empty or not set", flag, name)
	}
	return v, nil
}

func timeRange(own map[string]string) (from, to *time.Time, err error) {
	parse := func(flag string) (*time.Time, error) {
		if own[flag] == "" {
			return nil, nil
		}
		t, err := time.Parse(time.RFC3339Nano, own[flag])
		if err != nil {
			return nil, fmt.Errorf("--%s: use RFC 3339, e.g. 2026-01-02T15:04:05Z", flag)
		}
		return &t, nil
	}
	if from, err = parse("from"); err != nil {
		return nil, nil, err
	}
	to, err = parse("to")
	return from, to, err
}

func pageSize(own map[string]string) (int, error) {
	if own["page-size"] == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(own["page-size"])
	if err != nil || n < 1 {
		return 0, errors.New("--page-size must be a positive number")
	}
	return n, nil
}

func readSmallFile(path string) ([]byte, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only file
	b, err := io.ReadAll(io.LimitReader(f, maxCLIFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxCLIFileBytes {
		return nil, fmt.Errorf("%s is larger than 1 MiB", path)
	}
	return b, nil
}
