/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * OpenTelemetry metrics export over OTLP/HTTP (JSON encoding), with no
 * SDK dependency: the exporter renders the standard ExportMetricsService
 * request by hand and pushes cumulative counters on an interval. It is
 * configured only through the OpenTelemetry environment contract
 * (OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_METRICS_ENDPOINT,
 * OTEL_EXPORTER_OTLP_HEADERS, OTEL_SERVICE_NAME, OTEL_METRIC_EXPORT_INTERVAL,
 * OTEL_RESOURCE_ATTRIBUTES), so a collector already wired for other
 * services receives ChatCLI's tokens, cache, compaction, embedding and
 * cost counters without ChatCLI-specific settings. Off unless an
 * endpoint is set.
 */
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Standard OpenTelemetry environment variables honored by the exporter.
const (
	EnvEndpoint        = "OTEL_EXPORTER_OTLP_ENDPOINT"
	EnvMetricsEndpoint = "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"
	EnvHeaders         = "OTEL_EXPORTER_OTLP_HEADERS"
	EnvServiceName     = "OTEL_SERVICE_NAME"
	EnvExportInterval  = "OTEL_METRIC_EXPORT_INTERVAL" // milliseconds
	EnvResourceAttrs   = "OTEL_RESOURCE_ATTRIBUTES"

	defaultServiceName = "chatcli"
	defaultInterval    = 60 * time.Second
	scopeName          = "github.com/diillson/chatcli"
	pushTimeout        = 10 * time.Second
)

// Point is one cumulative counter value with its attributes.
type Point struct {
	Value float64
	Attrs map[string]string
}

// Metric is one cumulative sum metric.
type Metric struct {
	Name   string
	Unit   string
	Points []Point
}

// Source produces the current cumulative metrics on every export.
type Source func() []Metric

// Exporter pushes metrics to an OTLP/HTTP endpoint on an interval.
type Exporter struct {
	endpoint string
	headers  map[string]string
	resource map[string]string
	interval time.Duration
	client   *http.Client
	source   Source
	start    time.Time

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	lastErr error
	pushes  int
	logf    func(format string, args ...interface{})
}

// Enabled reports whether an OTLP endpoint is configured.
func Enabled() bool {
	return MetricsEndpoint() != ""
}

// MetricsEndpoint resolves the metrics URL per the OTLP spec: the
// signal-specific variable as is, else the base endpoint + /v1/metrics.
func MetricsEndpoint() string {
	if v := strings.TrimSpace(os.Getenv(EnvMetricsEndpoint)); v != "" {
		return v
	}
	base := strings.TrimSpace(os.Getenv(EnvEndpoint))
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/v1/metrics"
}

// NewFromEnv builds the exporter from the OTEL_* environment; nil when
// no endpoint is configured. extraResource attributes (surface, tenant)
// are merged over OTEL_RESOURCE_ATTRIBUTES.
func NewFromEnv(source Source, extraResource map[string]string) *Exporter {
	endpoint := MetricsEndpoint()
	if endpoint == "" || source == nil {
		return nil
	}
	e := &Exporter{
		endpoint: endpoint,
		headers:  parseHeaders(os.Getenv(EnvHeaders)),
		resource: map[string]string{"service.name": defaultServiceName},
		interval: defaultInterval,
		client:   &http.Client{Timeout: pushTimeout},
		source:   source,
		start:    time.Now(),
	}
	for k, v := range parseKV(os.Getenv(EnvResourceAttrs)) {
		e.resource[k] = v
	}
	if name := strings.TrimSpace(os.Getenv(EnvServiceName)); name != "" {
		e.resource["service.name"] = name
	}
	for k, v := range extraResource {
		if v != "" {
			e.resource[k] = v
		}
	}
	if ms, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvExportInterval))); err == nil && ms >= 1000 {
		e.interval = time.Duration(ms) * time.Millisecond
	}
	return e
}

// SetLogger installs a debug logger for push failures (optional).
func (e *Exporter) SetLogger(logf func(format string, args ...interface{})) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.logf = logf
	e.mu.Unlock()
}

// SetResource updates one resource attribute (surface, tenant) for later pushes.
func (e *Exporter) SetResource(key, value string) {
	if e == nil || key == "" {
		return
	}
	e.mu.Lock()
	if value == "" {
		delete(e.resource, key)
	} else {
		e.resource[key] = value
	}
	e.mu.Unlock()
}

// Start runs the export loop until Stop (or ctx ends); idempotent.
func (e *Exporter) Start(ctx context.Context) {
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.cancel != nil {
		e.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.done = make(chan struct{})
	e.mu.Unlock()
	go e.loop(ctx)
}

// Stop stops the loop and pushes a final snapshot bounded by ctx.
func (e *Exporter) Stop(ctx context.Context) {
	if e == nil {
		return
	}
	e.mu.Lock()
	cancel, done := e.cancel, e.done
	e.cancel = nil
	e.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	pushCtx, c := context.WithTimeout(ctx, pushTimeout)
	defer c()
	_ = e.Push(pushCtx)
}

func (e *Exporter) loop(ctx context.Context) {
	defer close(e.done)
	t := time.NewTicker(e.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := e.Push(ctx); err != nil {
				e.mu.Lock()
				logf := e.logf
				e.mu.Unlock()
				if logf != nil {
					logf("otlp metrics push failed: %v", err)
				}
			}
		}
	}
}

// Push renders and sends one ExportMetricsServiceRequest now.
func (e *Exporter) Push(ctx context.Context) error {
	if e == nil {
		return nil
	}
	body, err := e.render(e.source(), time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	e.mu.Lock()
	for k, v := range e.headers {
		req.Header.Set(k, v)
	}
	e.mu.Unlock()
	resp, err := e.client.Do(req)
	if err != nil {
		e.note(err)
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		err = fmt.Errorf("otlp endpoint returned HTTP %d", resp.StatusCode)
		e.note(err)
		return err
	}
	e.note(nil)
	return nil
}

func (e *Exporter) note(err error) {
	e.mu.Lock()
	e.lastErr = err
	if err == nil {
		e.pushes++
	}
	e.mu.Unlock()
}

// Status reports pushes so far and the last error ("" when healthy).
func (e *Exporter) Status() (endpoint string, pushes int, lastErr string) {
	if e == nil {
		return "", 0, ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastErr != nil {
		lastErr = e.lastErr.Error()
	}
	return e.endpoint, e.pushes, lastErr
}

// ---- OTLP/JSON rendering (metrics.proto JSON mapping) ----

type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue string `json:"stringValue"`
}

type otlpDataPoint struct {
	Attributes        []otlpKeyValue `json:"attributes,omitempty"`
	StartTimeUnixNano string         `json:"startTimeUnixNano"`
	TimeUnixNano      string         `json:"timeUnixNano"`
	AsDouble          float64        `json:"asDouble"`
}

type otlpSum struct {
	DataPoints             []otlpDataPoint `json:"dataPoints"`
	AggregationTemporality int             `json:"aggregationTemporality"` // 2 = CUMULATIVE
	IsMonotonic            bool            `json:"isMonotonic"`
}

type otlpMetric struct {
	Name string  `json:"name"`
	Unit string  `json:"unit,omitempty"`
	Sum  otlpSum `json:"sum"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type otlpScopeMetrics struct {
	Scope   otlpScope    `json:"scope"`
	Metrics []otlpMetric `json:"metrics"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpResourceMetrics struct {
	Resource     otlpResource       `json:"resource"`
	ScopeMetrics []otlpScopeMetrics `json:"scopeMetrics"`
}

type otlpExportRequest struct {
	ResourceMetrics []otlpResourceMetrics `json:"resourceMetrics"`
}

func (e *Exporter) render(metrics []Metric, now time.Time) ([]byte, error) {
	e.mu.Lock()
	res := sortedKV(e.resource)
	e.mu.Unlock()
	startNano := strconv.FormatInt(e.start.UnixNano(), 10)
	nowNano := strconv.FormatInt(now.UnixNano(), 10)
	out := make([]otlpMetric, 0, len(metrics))
	for _, m := range metrics {
		if m.Name == "" || len(m.Points) == 0 {
			continue
		}
		pts := make([]otlpDataPoint, 0, len(m.Points))
		for _, p := range m.Points {
			pts = append(pts, otlpDataPoint{Attributes: sortedKV(p.Attrs), StartTimeUnixNano: startNano, TimeUnixNano: nowNano, AsDouble: p.Value})
		}
		out = append(out, otlpMetric{Name: m.Name, Unit: m.Unit, Sum: otlpSum{DataPoints: pts, AggregationTemporality: 2, IsMonotonic: true}})
	}
	req := otlpExportRequest{ResourceMetrics: []otlpResourceMetrics{{
		Resource:     otlpResource{Attributes: res},
		ScopeMetrics: []otlpScopeMetrics{{Scope: otlpScope{Name: scopeName}, Metrics: out}},
	}}}
	return json.Marshal(req)
}

func sortedKV(m map[string]string) []otlpKeyValue {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]otlpKeyValue, 0, len(keys))
	for _, k := range keys {
		out = append(out, otlpKeyValue{Key: k, Value: otlpAnyValue{StringValue: m[k]}})
	}
	return out
}

// parseHeaders parses OTEL_EXPORTER_OTLP_HEADERS ("k=v,k2=v2", values
// URL-encoded per the spec).
func parseHeaders(raw string) map[string]string {
	out := parseKV(raw)
	for k, v := range out {
		if dec, err := url.QueryUnescape(v); err == nil {
			out[k] = dec
		}
	}
	return out
}

func parseKV(raw string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(pair, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if ok && k != "" {
			out[k] = v
		}
	}
	return out
}
