// Package observability provides content-free process metrics and tracing.
package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OrlojHQ/meridian/internal/buildinfo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/OrlojHQ/meridian"

// Config controls the optional, separate observability endpoints.
type Config struct {
	MetricsListen          string
	AllowUnsafeMetricsBind bool
	OTLPEndpoint           string
	OTLPInsecure           bool
}

// ValidateMetricsListen rejects accidental network exposure. An empty address
// disables metrics.
func ValidateMetricsListen(address string, allowUnsafe bool) error {
	if address == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid metrics listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if !allowUnsafe && (ip == nil || !ip.IsLoopback()) {
		return errors.New("metrics listener must use a literal loopback IP; set --allow-unsafe-metrics-listen to acknowledge network exposure")
	}
	return nil
}

// SetupTracing configures an OTLP/HTTP batch exporter. Empty endpoint disables
// exporting while retaining safe no-op spans.
func SetupTracing(ctx context.Context, config Config) (func(context.Context) error, error) {
	if config.OTLPEndpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	host, port, err := net.SplitHostPort(config.OTLPEndpoint)
	if err != nil || host == "" || port == "" || strings.ContainsAny(config.OTLPEndpoint, "\x00\r\n") {
		return nil, errors.New("OTLP endpoint must be a host:port without a URL scheme")
	}
	options := []otlptracehttp.Option{otlptracehttp.WithEndpoint(config.OTLPEndpoint)}
	if config.OTLPInsecure {
		options = append(options, otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("initialize OTLP trace exporter: %w", err)
	}
	info := buildinfo.Current()
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("meridiand"),
			semconv.ServiceVersion(info.Version),
			attribute.String("service.instance.scope", "single-user"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("initialize trace resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(2*time.Second),
			sdktrace.WithExportTimeout(3*time.Second),
			sdktrace.WithMaxQueueSize(1024),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.1))),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

// StartSpan starts a span whose name and attributes are selected only from
// fixed internal vocabulary. Callers must not pass IDs, paths, content, or
// arbitrary error text.
func StartSpan(ctx context.Context, name string, attributes ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(instrumentationName).Start(ctx, name, trace.WithAttributes(attributes...))
}

// Metrics is a bounded in-memory Prometheus text collector. Every dimension is
// normalized to a fixed allowlist before becoming a label.
type Metrics struct {
	mu        sync.Mutex
	counters  map[string]float64
	sums      map[string]float64
	counts    map[string]uint64
	activePTY atomic.Int64
	previews  atomic.Int64
	ready     atomic.Bool
	healthy   atomic.Bool
}

func NewMetrics() *Metrics {
	metrics := &Metrics{
		counters: make(map[string]float64),
		sums:     make(map[string]float64),
		counts:   make(map[string]uint64),
	}
	metrics.healthy.Store(true)
	return metrics
}

func (m *Metrics) SetReady(value bool)   { m.ready.Store(value) }
func (m *Metrics) SetHealthy(value bool) { m.healthy.Store(value) }

func (m *Metrics) ObserveReconcile(duration time.Duration, result string) {
	m.observe("meridian_reconcile_duration_seconds", labels("result", resultLabel(result)), duration.Seconds())
}

func (m *Metrics) ProviderOperation(provider, operation, result string, duration time.Duration) {
	dimensions := labels(
		"provider", enum(provider, "fake", "docker", "agentsandbox"),
		"operation", enum(operation, "capabilities", "create", "get", "pause", "resume", "delete", "capture", "restore", "run", "structured", "cancel", "attach", "preview"),
		"result", resultLabel(result),
	)
	m.increment("meridian_provider_operations_total", dimensions, 1)
	m.observe("meridian_provider_operation_duration_seconds", dimensions, duration.Seconds())
}

func (m *Metrics) Retry(operation string) {
	m.increment("meridian_retries_total", labels("operation", enum(operation, "reconcile", "cleanup", "provider")), 1)
}

func (m *Metrics) Cleanup(provider, result string) {
	m.increment("meridian_leaked_resource_cleanup_total", labels(
		"provider", enum(provider, "fake", "docker", "agentsandbox"),
		"result", resultLabel(result),
	), 1)
}

func (m *Metrics) PTY(delta int64)     { addNonNegative(&m.activePTY, delta) }
func (m *Metrics) Preview(delta int64) { addNonNegative(&m.previews, delta) }

func (m *Metrics) RunTransition(from, to string) {
	m.increment("meridian_run_transitions_total", labels(
		"from", enum(from, "Queued", "Starting", "Running", "Cancelling", "Succeeded", "Failed", "Cancelled"),
		"to", enum(to, "Queued", "Starting", "Running", "Cancelling", "Succeeded", "Failed", "Cancelled"),
	), 1)
}

func (m *Metrics) EventBackpressure(kind string) {
	m.increment("meridian_event_backpressure_total", labels("kind", enum(kind, "queue_full", "slow_client", "write_timeout")), 1)
}

func (m *Metrics) EventGap() {
	m.increment("meridian_event_replay_gaps_total", "", 1)
}

func (m *Metrics) Snapshot(duration time.Duration, size int64, result string) {
	dimensions := labels("result", resultLabel(result))
	m.observe("meridian_moment_snapshot_duration_seconds", dimensions, duration.Seconds())
	if size >= 0 {
		m.observe("meridian_moment_snapshot_size_bytes", dimensions, float64(size))
	}
}

func (m *Metrics) ArtifactFailure(kind string) {
	m.increment("meridian_artifact_failures_total", labels("kind", enum(kind, "missing", "corrupt", "publish", "retention")), 1)
}

func (m *Metrics) ArtifactRetention(action string, count int64) {
	if count > 0 {
		m.increment("meridian_artifact_retention_total", labels("action", enum(action, "verified", "eligible", "deleted")), float64(count))
	}
}

func (m *Metrics) HTTP(method, route string, status int, duration time.Duration) {
	m.observe("meridian_http_request_duration_seconds", labels(
		"method", enum(method, "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"),
		"route", routeLabel(route),
		"status_class", statusClass(status),
	), duration.Seconds())
}

func (m *Metrics) StartupRecovery(kind, result string, duration time.Duration) {
	m.observe("meridian_startup_recovery_duration_seconds", labels(
		"kind", enum(kind, "capsules", "runs", "threads"),
		"result", resultLabel(result),
	), duration.Seconds())
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/metrics" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = io.WriteString(writer, m.render())
	})
}

func (m *Metrics) render() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var output strings.Builder
	output.WriteString("# HELP meridian_health Process health state.\n# TYPE meridian_health gauge\n")
	writeGauge(&output, "meridian_health", boolFloat(m.healthy.Load()))
	output.WriteString("# HELP meridian_ready Readiness state after recovery.\n# TYPE meridian_ready gauge\n")
	writeGauge(&output, "meridian_ready", boolFloat(m.ready.Load()))
	output.WriteString("# HELP meridian_active_pty_sessions Current proxied PTY sessions.\n# TYPE meridian_active_pty_sessions gauge\n")
	writeGauge(&output, "meridian_active_pty_sessions", float64(m.activePTY.Load()))
	output.WriteString("# HELP meridian_active_preview_sessions Current preview proxy sessions.\n# TYPE meridian_active_preview_sessions gauge\n")
	writeGauge(&output, "meridian_active_preview_sessions", float64(m.previews.Load()))

	keys := make([]string, 0, len(m.counters))
	for key := range m.counters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lastName := ""
	for _, key := range keys {
		name, dimensions := splitKey(key)
		if name != lastName {
			fmt.Fprintf(&output, "# TYPE %s counter\n", name)
			lastName = name
		}
		fmt.Fprintf(&output, "%s%s %s\n", name, dimensions, formatFloat(m.counters[key]))
	}
	keys = keys[:0]
	for key := range m.counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lastName = ""
	for _, key := range keys {
		name, dimensions := splitKey(key)
		if name != lastName {
			fmt.Fprintf(&output, "# TYPE %s summary\n", name)
			lastName = name
		}
		fmt.Fprintf(&output, "%s_sum%s %s\n%s_count%s %d\n",
			name, dimensions, formatFloat(m.sums[key]), name, dimensions, m.counts[key])
	}
	return output.String()
}

func (m *Metrics) increment(name, dimensions string, value float64) {
	m.mu.Lock()
	m.counters[name+"|"+dimensions] += value
	m.mu.Unlock()
}

func (m *Metrics) observe(name, dimensions string, value float64) {
	if value < 0 {
		value = 0
	}
	m.mu.Lock()
	key := name + "|" + dimensions
	m.sums[key] += value
	m.counts[key]++
	m.mu.Unlock()
}

func addNonNegative(value *atomic.Int64, delta int64) {
	for {
		current := value.Load()
		next := current + delta
		if next < 0 {
			next = 0
		}
		if value.CompareAndSwap(current, next) {
			return
		}
	}
}

func labels(values ...string) string {
	var output strings.Builder
	output.WriteByte('{')
	for index := 0; index < len(values); index += 2 {
		if index > 0 {
			output.WriteByte(',')
		}
		fmt.Fprintf(&output, "%s=%q", values[index], values[index+1])
	}
	output.WriteByte('}')
	return output.String()
}

func enum(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return candidate
		}
	}
	return "other"
}

func resultLabel(value string) string {
	return enum(value, "ok", "error", "timeout", "cancelled", "not_found", "unsupported")
}

func routeLabel(value string) string {
	switch value {
	case "GET /healthz", "GET /readyz", "GET /capabilities",
		"POST /projects", "GET /projects", "GET /projects/{projectId}",
		"POST /projects/{projectId}/capsules", "GET /projects/{projectId}/capsules",
		"GET /capsules/{capsuleId}", "POST /capsules/{capsuleId}/pause",
		"POST /capsules/{capsuleId}/resume", "POST /capsules/{capsuleId}/delete",
		"POST /capsules/{capsuleId}/runs", "GET /capsules/{capsuleId}/runs",
		"GET /runs/{runId}", "POST /runs/{runId}/cancel",
		"GET /runs/{runId}/events", "GET /runs/{runId}/events/stream",
		"POST /runs/{runId}/attach-ticket", "GET /runs/{runId}/attach",
		"GET /capsules/{capsuleId}/git/status", "GET /capsules/{capsuleId}/git/diff",
		"POST /capsules/{capsuleId}/moments", "GET /capsules/{capsuleId}/moments",
		"GET /moments/{momentId}", "GET /timelines/{timelineId}",
		"POST /moments/{momentId}/shards", "POST /capsules/{capsuleId}/rewind",
		"POST /capsules/{capsuleId}/seal", "GET /capsules/{capsuleId}/previews",
		"POST /capsules/{capsuleId}/previews/{port}/tickets",
		"GET /capsules/{capsuleId}/harness-profiles",
		"POST /capsules/{capsuleId}/threads", "GET /capsules/{capsuleId}/threads",
		"GET /threads/{threadId}", "POST /threads/{threadId}/archive",
		"POST /threads/{threadId}/delete", "POST /threads/{threadId}/start",
		"POST /threads/{threadId}/resume", "POST /threads/{threadId}/messages",
		"POST /threads/{threadId}/responses", "POST /threads/{threadId}/cancel",
		"GET /threads/{threadId}/blocks", "GET /threads/{threadId}/blocks/stream":
		return value
	case "":
		return "unmatched"
	default:
		return "other"
	}
}

func statusClass(status int) string {
	if status < 100 || status > 599 {
		return "other"
	}
	return strconv.Itoa(status/100) + "xx"
}

func splitKey(key string) (string, string) {
	name, dimensions, _ := strings.Cut(key, "|")
	return name, dimensions
}

func formatFloat(value float64) string { return strconv.FormatFloat(value, 'g', -1, 64) }
func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func writeGauge(output *strings.Builder, name string, value float64) {
	fmt.Fprintf(output, "%s %s\n", name, formatFloat(value))
}
