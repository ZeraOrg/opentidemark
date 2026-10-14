// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/ingest"
	"github.com/ZeraOrg/opentidemark/internal/jsonx"
)

// Media types.
const (
	contentProtobuf    = "application/x-protobuf"
	contentProtobufAlt = "application/protobuf"
	contentJSON        = "application/json"
)

// HTTPHandler serves OTLP/HTTP below a prefix ("" or "/otlp").
type HTTPHandler struct {
	d      *ingest.Deps
	exp    *Exporter
	prefix string
}

// NewHTTPHandler returns an OTLP/HTTP handler.
func NewHTTPHandler(d *ingest.Deps, exp *Exporter, prefix string) *HTTPHandler {
	return &HTTPHandler{d: d, exp: exp, prefix: prefix}
}

// Register adds POST {prefix}/v1/logs|traces|metrics to mux.
func (h *HTTPHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST "+h.prefix+"/v1/logs", h.logs)
	mux.HandleFunc("POST "+h.prefix+"/v1/traces", h.traces)
	mux.HandleFunc("POST "+h.prefix+"/v1/metrics", h.metrics)
}

func (h *HTTPHandler) logs(w http.ResponseWriter, r *http.Request) {
	in := &collogspb.ExportLogsServiceRequest{}
	h.serve(w, r, in, func(ctx context.Context, req *ingest.Request) (proto.Message, error) {
		return h.exp.ExportLogs(ctx, req, in)
	})
}

func (h *HTTPHandler) traces(w http.ResponseWriter, r *http.Request) {
	in := &coltracepb.ExportTraceServiceRequest{}
	h.serve(w, r, in, func(ctx context.Context, req *ingest.Request) (proto.Message, error) {
		return h.exp.ExportTraces(ctx, req, in)
	})
}

func (h *HTTPHandler) metrics(w http.ResponseWriter, r *http.Request) {
	in := &colmetricspb.ExportMetricsServiceRequest{}
	h.serve(w, r, in, func(ctx context.Context, req *ingest.Request) (proto.Message, error) {
		return h.exp.ExportMetrics(ctx, req, in)
	})
}

type exportFunc func(ctx context.Context, req *ingest.Request) (proto.Message, error)

func (h *HTTPHandler) serve(w http.ResponseWriter, r *http.Request, in proto.Message, export exportFunc) {
	isJSON, ok := mediaType(r.Header.Get("Content-Type"))
	if !ok {
		writeStatus(w, false, http.StatusUnsupportedMediaType, codes.InvalidArgument, "unsupported content type")
		return
	}
	req, err := h.d.Authenticate(r, auth.ProtocolOTLP, false)
	if err != nil {
		writeError(w, isJSON, err)
		return
	}
	body, err := ingest.ReadBody(r, h.d.Limits, true)
	if err != nil {
		writeError(w, isJSON, err)
		return
	}
	if err := Unmarshal(body, isJSON, in); err != nil {
		writeError(w, isJSON, fmt.Errorf("%w: %w", ingest.ErrBadRequest, err))
		return
	}
	resp, err := export(r.Context(), req)
	if err != nil {
		writeError(w, isJSON, err)
		return
	}
	writeMessage(w, isJSON, http.StatusOK, resp)
}

func mediaType(ct string) (isJSON, ok bool) {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false, false
	}
	switch mt {
	case contentProtobuf, contentProtobufAlt:
		return false, true
	case contentJSON:
		return true, true
	default:
		return false, false
	}
}

// Unmarshal decodes an OTLP request from protobuf or OTLP/JSON (hex ids, lowerCamelCase).
func Unmarshal(body []byte, isJSON bool, msg proto.Message) error {
	if !isJSON {
		if err := proto.Unmarshal(body, msg); err != nil {
			return fmt.Errorf("invalid protobuf: %w", err)
		}
		return nil
	}
	v, err := jsonx.Decode(body, jsonx.MaxDepth)
	if err != nil {
		return err
	}
	fixed, err := json.Marshal(hexIDsToBase64(v))
	if err != nil {
		return fmt.Errorf("re-encode json: %w", err)
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(fixed, msg); err != nil {
		return fmt.Errorf("invalid OTLP JSON: %w", err)
	}
	return nil
}

// idKeys are the OTLP/JSON fields encoded as hex instead of protobuf-JSON base64.
var idKeys = map[string]bool{"traceId": true, "spanId": true, "parentSpanId": true}

func hexIDsToBase64(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && idKeys[k] {
				if b, err := hex.DecodeString(s); err == nil {
					x[k] = base64.StdEncoding.EncodeToString(b)
				}
				continue
			}
			x[k] = hexIDsToBase64(val)
		}
	case []any:
		for i := range x {
			x[i] = hexIDsToBase64(x[i])
		}
	}
	return v
}

func writeMessage(w http.ResponseWriter, isJSON bool, code int, msg proto.Message) {
	var body []byte
	var err error
	if isJSON {
		w.Header().Set("Content-Type", contentJSON)
		body, err = protojson.Marshal(msg)
	} else {
		w.Header().Set("Content-Type", contentProtobuf)
		body, err = proto.Marshal(msg)
	}
	if err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(code)
	_, _ = w.Write(body) // the client may have gone away; nothing useful to do
}

func writeError(w http.ResponseWriter, isJSON bool, err error) {
	ingest.SetRetryHeaders(w, err)
	st := ingest.Status(err)
	writeStatus(w, isJSON, st, grpcCode(err), ingest.PublicMessage(err))
}

func writeStatus(w http.ResponseWriter, isJSON bool, httpStatus int, code codes.Code, msg string) {
	writeMessage(w, isJSON, httpStatus, status.New(code, msg).Proto())
}
