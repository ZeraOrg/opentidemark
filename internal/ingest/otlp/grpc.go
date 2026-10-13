// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	_ "google.golang.org/grpc/encoding/gzip" // registers the gzip compressor (SPEC §9.4)
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/ingest"
)

// GRPCService implements the three OTLP collector services.
type GRPCService struct {
	d   *ingest.Deps
	exp *Exporter
}

// RegisterGRPC registers the OTLP services on s.
func RegisterGRPC(s *grpc.Server, d *ingest.Deps, exp *Exporter) {
	svc := &GRPCService{d: d, exp: exp}
	collogspb.RegisterLogsServiceServer(s, logsServer{GRPCService: svc})
	coltracepb.RegisterTraceServiceServer(s, traceServer{GRPCService: svc})
	colmetricspb.RegisterMetricsServiceServer(s, metricsServer{GRPCService: svc})
}

type logsServer struct {
	collogspb.UnimplementedLogsServiceServer
	*GRPCService
}

func (l logsServer) Export(ctx context.Context, in *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	req, err := l.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := l.exp.ExportLogs(ctx, req, in)
	return resp, toGRPCError(err)
}

type traceServer struct {
	coltracepb.UnimplementedTraceServiceServer
	*GRPCService
}

func (t traceServer) Export(ctx context.Context, in *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	req, err := t.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := t.exp.ExportTraces(ctx, req, in)
	return resp, toGRPCError(err)
}

type metricsServer struct {
	colmetricspb.UnimplementedMetricsServiceServer
	*GRPCService
}

func (m metricsServer) Export(ctx context.Context, in *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	req, err := m.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := m.exp.ExportMetrics(ctx, req, in)
	return resp, toGRPCError(err)
}

func (s *GRPCService) authenticate(ctx context.Context) (*ingest.Request, error) {
	req, err := s.d.AuthenticateRaw(ctx, keyFromMetadata(ctx), auth.ProtocolOTLP, peerAddr(ctx))
	return req, toGRPCError(err)
}

// keyFromMetadata reads x-tidemark-key, then "authorization: Bearer <key>".
func keyFromMetadata(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("x-tidemark-key"); len(v) > 0 && v[0] != "" {
		return strings.TrimSpace(v[0])
	}
	for _, v := range md.Get("authorization") {
		if scheme, key, ok := strings.Cut(strings.TrimSpace(v), " "); ok && strings.EqualFold(scheme, "bearer") {
			return strings.TrimSpace(key)
		}
	}
	return ""
}

func peerAddr(ctx context.Context) netip.Addr {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return netip.Addr{}
	}
	if ap, err := netip.ParseAddrPort(p.Addr.String()); err == nil {
		return ap.Addr().Unmap()
	}
	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		return netip.Addr{}
	}
	a, _ := netip.ParseAddr(host)
	return a.Unmap()
}

// grpcCode maps ingest errors to gRPC codes (backpressure ⇒ UNAVAILABLE, SPEC §9.1).
func grpcCode(err error) codes.Code {
	switch ingest.Status(err) {
	case http.StatusUnauthorized:
		return codes.Unauthenticated
	case http.StatusForbidden:
		return codes.PermissionDenied
	case http.StatusServiceUnavailable:
		return codes.Unavailable
	case http.StatusTooManyRequests:
		return codes.ResourceExhausted
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType:
		return codes.InvalidArgument
	default:
		return codes.Internal
	}
}

func toGRPCError(err error) error {
	if err == nil {
		return nil
	}
	return status.Error(grpcCode(err), ingest.PublicMessage(err))
}
