// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"maps"
	"math"
	"strconv"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/ZeraOrg/opentidemark/internal/model"
)

// noRecordedValue marks data points that carry no value.
const noRecordedValue = uint32(metricspb.DataPointFlags_DATA_POINT_FLAGS_NO_RECORDED_VALUE_MASK)

// metricCtx carries the per-metric fields shared by its points.
type metricCtx struct {
	res         resourceInfo
	m           *metricspb.Metric
	kind        string
	temporality string
	monotonic   bool
}

// MetricsToPoints maps an OTLP metrics request to metric points (SPEC §9.4 Metrics).
func MetricsToPoints(req *colmetricspb.ExportMetricsServiceRequest) []model.MetricPoint {
	var out []model.MetricPoint
	for _, rm := range req.GetResourceMetrics() {
		res := newResourceInfo(rm.GetResource())
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				out = append(out, metricPoints(res, m)...)
			}
		}
	}
	return out
}

func metricPoints(res resourceInfo, m *metricspb.Metric) []model.MetricPoint {
	c := metricCtx{res: res, m: m}
	var out []model.MetricPoint
	switch d := m.Data.(type) {
	case *metricspb.Metric_Gauge:
		c.kind = model.MetricGauge
		out = numberPoints(c, d.Gauge.GetDataPoints())
	case *metricspb.Metric_Sum:
		c.kind, c.temporality, c.monotonic = model.MetricSum, temporality(d.Sum.GetAggregationTemporality()), d.Sum.GetIsMonotonic()
		out = numberPoints(c, d.Sum.GetDataPoints())
	case *metricspb.Metric_Histogram:
		c.kind, c.temporality = model.MetricHistogram, temporality(d.Histogram.GetAggregationTemporality())
		for _, p := range d.Histogram.GetDataPoints() {
			if p.GetFlags()&noRecordedValue == 0 {
				out = append(out, histogramPoint(c, p))
			}
		}
	case *metricspb.Metric_ExponentialHistogram:
		c.kind, c.temporality = model.MetricExpHistogram, temporality(d.ExponentialHistogram.GetAggregationTemporality())
		for _, p := range d.ExponentialHistogram.GetDataPoints() {
			if p.GetFlags()&noRecordedValue == 0 {
				out = append(out, expHistogramPoint(c, p))
			}
		}
	case *metricspb.Metric_Summary:
		c.kind, c.temporality = model.MetricSummary, model.TemporalityCumulative // OTLP summaries are cumulative
		for _, p := range d.Summary.GetDataPoints() {
			if p.GetFlags()&noRecordedValue == 0 {
				out = append(out, summaryPoint(c, p))
			}
		}
	}
	return out
}

// base builds the common fields; resource attributes are merged under point attributes.
func (c metricCtx) base(start, ts uint64, pointAttrs []*commonpb.KeyValue) model.MetricPoint {
	a := make(map[string]any, len(c.res.attrs)+len(pointAttrs))
	maps.Copy(a, c.res.attrs)
	delete(a, attrService)
	maps.Copy(a, attrs(pointAttrs))
	return model.MetricPoint{
		Timestamp: nanos(ts), StartTime: nanos(start), Stream: c.res.stream, Service: c.res.service, Name: c.m.GetName(),
		Description: c.m.GetDescription(), Unit: c.m.GetUnit(), Kind: c.kind, Temporality: c.temporality,
		IsMonotonic: c.monotonic, Attributes: a,
	}
}

func temporality(t metricspb.AggregationTemporality) string {
	switch t {
	case metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA:
		return model.TemporalityDelta
	case metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE:
		return model.TemporalityCumulative
	default:
		return ""
	}
}

func numberPoints(c metricCtx, pts []*metricspb.NumberDataPoint) []model.MetricPoint {
	out := make([]model.MetricPoint, 0, len(pts))
	for _, p := range pts {
		if p.GetFlags()&noRecordedValue != 0 {
			continue
		}
		mp := c.base(p.GetStartTimeUnixNano(), p.GetTimeUnixNano(), p.GetAttributes())
		switch v := p.Value.(type) {
		case *metricspb.NumberDataPoint_AsDouble:
			mp.Value = v.AsDouble
		case *metricspb.NumberDataPoint_AsInt:
			mp.Value = float64(v.AsInt)
		}
		out = append(out, mp)
	}
	return out
}

func histogramPoint(c metricCtx, p *metricspb.HistogramDataPoint) model.MetricPoint {
	mp := c.base(p.GetStartTimeUnixNano(), p.GetTimeUnixNano(), p.GetAttributes())
	mp.Count, mp.Sum, mp.Min, mp.Max = p.GetCount(), p.GetSum(), p.GetMin(), p.GetMax()
	mp.BucketBounds, mp.BucketCounts = p.GetExplicitBounds(), p.GetBucketCounts()
	mp.Value = mean(mp.Sum, mp.Count)
	return mp
}

func expHistogramPoint(c metricCtx, p *metricspb.ExponentialHistogramDataPoint) model.MetricPoint {
	mp := c.base(p.GetStartTimeUnixNano(), p.GetTimeUnixNano(), p.GetAttributes())
	mp.Count, mp.Sum, mp.Min, mp.Max = p.GetCount(), p.GetSum(), p.GetMin(), p.GetMax()
	mp.BucketBounds, mp.BucketCounts = ExpToExplicit(p.GetScale(), p.GetZeroCount(), p.GetPositive(), p.GetNegative())
	mp.Value = mean(mp.Sum, mp.Count)
	return mp
}

// ExpToExplicit converts exponential buckets to explicit upper bounds (base^i, base =
// 2^(2^-scale)). The result follows explicit-histogram semantics: len(counts) = len(bounds)+1
// and the final (overflow) bucket is empty.
func ExpToExplicit(scale int32, zeroCount uint64, pos, neg *metricspb.ExponentialHistogramDataPoint_Buckets) ([]float64, []uint64) {
	base := math.Pow(2, math.Pow(2, -float64(scale)))
	var bounds []float64
	var counts []uint64
	negCounts := neg.GetBucketCounts()
	for k := len(negCounts) - 1; k >= 0; k-- {
		bounds = append(bounds, -math.Pow(base, float64(neg.GetOffset())+float64(k)))
		counts = append(counts, negCounts[k])
	}
	if zeroCount > 0 || len(negCounts) > 0 {
		bounds = append(bounds, 0)
		counts = append(counts, zeroCount)
	}
	for k, n := range pos.GetBucketCounts() {
		bounds = append(bounds, math.Pow(base, float64(pos.GetOffset())+float64(k)+1))
		counts = append(counts, n)
	}
	return bounds, append(counts, 0)
}

func summaryPoint(c metricCtx, p *metricspb.SummaryDataPoint) model.MetricPoint {
	mp := c.base(p.GetStartTimeUnixNano(), p.GetTimeUnixNano(), p.GetAttributes())
	mp.Count, mp.Sum = p.GetCount(), p.GetSum()
	mp.Value = mean(mp.Sum, mp.Count)
	for _, q := range p.GetQuantileValues() {
		mp.Attributes[model.SummaryQuantilePrefix+strconv.FormatFloat(q.GetQuantile(), 'f', -1, 64)] = q.GetValue()
	}
	return mp
}

func mean(sum float64, count uint64) float64 {
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}
