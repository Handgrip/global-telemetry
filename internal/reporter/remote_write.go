package reporter

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/golang/snappy"
	"github.com/Handgrip/global-telemetry/internal/config"
	"github.com/Handgrip/global-telemetry/internal/probe"
)

// Reporter converts raw probe results into time series and pushes them.
type Reporter interface {
	Report(ctx context.Context, results []*probe.RawResult) error
}

// PrometheusReporter pushes per-cycle summary metrics via Prometheus Remote Write.
type PrometheusReporter struct {
	remoteWriteURL string
	username       string
	apiKey         string
	probeName      string
	metricPrefix   string
	httpClient     *http.Client
}

func NewPrometheusReporter(cfg config.GrafanaCloudConfig, probeName, metricPrefix string) *PrometheusReporter {
	if metricPrefix == "" {
		metricPrefix = "gt_"
	}
	return &PrometheusReporter{
		remoteWriteURL: cfg.RemoteWriteURL,
		username:       cfg.Username,
		apiKey:         cfg.APIKey,
		probeName:      probeName,
		metricPrefix:   metricPrefix,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (r *PrometheusReporter) Report(ctx context.Context, results []*probe.RawResult) error {
	if len(results) == 0 {
		return nil
	}

	var allSeries []timeSeries
	for _, result := range results {
		allSeries = append(allSeries, r.resultToTimeSeries(result)...)
	}

	if len(allSeries) == 0 {
		return nil
	}

	data := marshalWriteRequest(allSeries)
	compressed := snappy.Encode(nil, data)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.remoteWriteURL, bytes.NewReader(compressed))
	if err != nil {
		return fmt.Errorf("build remote write request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	req.SetBasicAuth(r.username, r.apiKey)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("remote write request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("remote write rejected: status=%d body=%s", resp.StatusCode, string(body))
	}

	slog.Debug("remote write success", "series", len(allSeries), "bytes", len(compressed))
	return nil
}

var reservedLabels = map[string]bool{
	"__name__": true,
	"probe":    true,
	"target":   true,
	"type":     true,
	"host":     true,
	"url":      true,
}

func (r *PrometheusReporter) baseLabels(result *probe.RawResult) []label {
	labels := []label{
		{Name: "probe", Value: r.probeName},
		{Name: "target", Value: result.Target.Name},
		{Name: "type", Value: result.Target.Type},
	}
	if result.Target.Host != "" {
		labels = append(labels, label{Name: "host", Value: result.Target.Host})
	}
	if result.Target.URL != "" {
		labels = append(labels, label{Name: "url", Value: result.Target.URL})
	}
	for k, v := range result.Target.Labels {
		if reservedLabels[k] {
			slog.Warn("skipping user label that conflicts with reserved name", "label", k, "target", result.Target.Name)
			continue
		}
		labels = append(labels, label{Name: k, Value: v})
	}
	return labels
}

func (r *PrometheusReporter) makeSeries(name string, value float64, ts time.Time, baseLabels []label) timeSeries {
	labels := make([]label, 0, len(baseLabels)+1)
	labels = append(labels, label{Name: "__name__", Value: r.metricPrefix + name})
	labels = append(labels, baseLabels...)
	sort.Slice(labels, func(i, j int) bool {
		return labels[i].Name < labels[j].Name
	})
	return timeSeries{
		Labels:  labels,
		Samples: []sample{{Value: value, TimestampMs: ts.UnixMilli()}},
	}
}

func (r *PrometheusReporter) resultToTimeSeries(result *probe.RawResult) []timeSeries {
	base := r.baseLabels(result)
	ts := result.Timestamp
	var series []timeSeries

	successVal := 0.0
	if result.Success {
		successVal = 1.0
	}
	series = append(series, r.makeSeries("probe_success", successVal, ts, base))

	if result.ICMP != nil {
		series = append(series, r.icmpToTimeSeries(result.ICMP, ts, base)...)
	}
	if result.HTTP != nil {
		series = append(series, r.httpToTimeSeries(result.HTTP, ts, base)...)
	}

	return series
}

func (r *PrometheusReporter) icmpToTimeSeries(icmp *probe.ICMPRawResult, ts time.Time, base []label) []timeSeries {
	var series []timeSeries

	lossRatio := 1.0
	if icmp.PacketsSent > 0 {
		lossRatio = 1.0 - float64(icmp.PacketsRecv)/float64(icmp.PacketsSent)
	}
	series = append(series, r.makeSeries("probe_icmp_packet_loss_ratio", lossRatio, ts, base))

	if len(icmp.PacketRTTs) > 0 {
		var sum, minRTT, maxRTT time.Duration
		minRTT = icmp.PacketRTTs[0]
		maxRTT = icmp.PacketRTTs[0]
		for _, rtt := range icmp.PacketRTTs {
			sum += rtt
			if rtt < minRTT {
				minRTT = rtt
			}
			if rtt > maxRTT {
				maxRTT = rtt
			}
		}
		avg := sum / time.Duration(len(icmp.PacketRTTs))
		series = append(series,
			r.makeSeries("probe_icmp_rtt_avg_seconds", avg.Seconds(), ts, base),
			r.makeSeries("probe_icmp_rtt_min_seconds", minRTT.Seconds(), ts, base),
			r.makeSeries("probe_icmp_rtt_max_seconds", maxRTT.Seconds(), ts, base),
		)
	}

	return series
}

func (r *PrometheusReporter) httpToTimeSeries(h *probe.HTTPRawResult, ts time.Time, base []label) []timeSeries {
	series := []timeSeries{
		r.makeSeries("probe_http_status_code", float64(h.StatusCode), ts, base),
		r.makeSeries("probe_http_duration_seconds", h.TotalDuration.Seconds(), ts, base),
	}

	if h.DNSDuration > 0 {
		series = append(series, r.makeSeries("probe_http_dns_seconds", h.DNSDuration.Seconds(), ts, base))
	}
	if h.ConnDuration > 0 {
		series = append(series, r.makeSeries("probe_http_connect_seconds", h.ConnDuration.Seconds(), ts, base))
	}
	if h.TLSDuration > 0 {
		series = append(series, r.makeSeries("probe_http_tls_seconds", h.TLSDuration.Seconds(), ts, base))
	}
	if h.TTFBDuration > 0 {
		series = append(series, r.makeSeries("probe_http_ttfb_seconds", h.TTFBDuration.Seconds(), ts, base))
	}
	if !h.TLSExpiry.IsZero() {
		expirySeconds := time.Until(h.TLSExpiry).Seconds()
		series = append(series, r.makeSeries("probe_http_tls_expiry_seconds", expirySeconds, ts, base))
	}

	return series
}

// --- Minimal Prometheus Remote Write protobuf encoding ---
// Implements the WriteRequest wire format without any protobuf library.

type label struct {
	Name  string
	Value string
}

type sample struct {
	Value       float64
	TimestampMs int64
}

type timeSeries struct {
	Labels  []label
	Samples []sample
}

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func appendField(b []byte, fieldNum int, wireType int, data []byte) []byte {
	b = appendVarint(b, uint64(fieldNum<<3|wireType))
	if wireType == 2 {
		b = appendVarint(b, uint64(len(data)))
	}
	return append(b, data...)
}

func appendStringField(b []byte, fieldNum int, s string) []byte {
	b = appendVarint(b, uint64(fieldNum<<3|2))
	b = appendVarint(b, uint64(len(s)))
	return append(b, s...)
}

func marshalLabel(l label) []byte {
	var b []byte
	b = appendStringField(b, 1, l.Name)
	b = appendStringField(b, 2, l.Value)
	return b
}

func marshalSample(s sample) []byte {
	var b []byte
	// field 1: double value (wire type 1 = 64-bit)
	b = appendVarint(b, uint64(1<<3|1))
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], math.Float64bits(s.Value))
	b = append(b, buf[:]...)
	// field 2: int64 timestamp_ms (wire type 0 = varint, tag = 2<<3|0)
	b = appendVarint(b, uint64(2<<3))
	b = appendVarint(b, uint64(s.TimestampMs))
	return b
}

func marshalTimeSeries(ts timeSeries) []byte {
	var b []byte
	for _, l := range ts.Labels {
		lb := marshalLabel(l)
		b = appendField(b, 1, 2, lb)
	}
	for _, s := range ts.Samples {
		sb := marshalSample(s)
		b = appendField(b, 2, 2, sb)
	}
	return b
}

func marshalWriteRequest(series []timeSeries) []byte {
	var b []byte
	for _, ts := range series {
		tsb := marshalTimeSeries(ts)
		b = appendField(b, 1, 2, tsb)
	}
	return b
}
