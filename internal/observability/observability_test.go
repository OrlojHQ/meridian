package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateMetricsListen(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"", "127.0.0.1:9090", "[::1]:9090"} {
		if err := ValidateMetricsListen(address, false); err != nil {
			t.Fatalf("ValidateMetricsListen(%q): %v", address, err)
		}
	}
	for _, address := range []string{"localhost:9090", "0.0.0.0:9090", ":9090", "192.0.2.1:9090"} {
		if err := ValidateMetricsListen(address, false); err == nil {
			t.Fatalf("ValidateMetricsListen(%q) unexpectedly succeeded", address)
		}
	}
	if err := ValidateMetricsListen("0.0.0.0:9090", true); err != nil {
		t.Fatalf("explicit unsafe opt-in was rejected: %v", err)
	}
}

func TestMetricsNeverExposeDynamicContent(t *testing.T) {
	t.Parallel()
	metrics := NewMetrics()
	secret := "capsule-123/repository/private?token=secret"
	metrics.ProviderOperation(secret, secret, secret, time.Second)
	metrics.HTTP(secret, secret, 999, time.Second)
	metrics.RunTransition(secret, secret)
	metrics.ArtifactFailure(secret)
	metrics.StartupRecovery(secret, secret, time.Second)

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, request)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if strings.Contains(body, secret) || strings.Contains(body, "capsule-123") || strings.Contains(body, "token=secret") {
		t.Fatalf("metrics exposed dynamic content:\n%s", body)
	}
	if got := strings.Count(body, `provider="other"`); got != 3 {
		t.Fatalf("provider series count = %d, want 3 metric forms", got)
	}
}

func TestGaugeBoundsAndDisabledTracingShutdown(t *testing.T) {
	t.Parallel()
	metrics := NewMetrics()
	metrics.PTY(-10)
	metrics.Preview(-1)
	metrics.PTY(1)
	metrics.PTY(-2)
	body := metrics.render()
	if !strings.Contains(body, "meridian_active_pty_sessions 0") ||
		!strings.Contains(body, "meridian_active_preview_sessions 0") {
		t.Fatalf("gauges were not bounded:\n%s", body)
	}
	shutdown, err := SetupTracing(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}

func TestTracingRejectsMalformedExporterEndpoint(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	shutdown, err := SetupTracing(ctx, Config{OTLPEndpoint: "bad endpoint\n"})
	if err == nil {
		_ = shutdown(ctx)
		t.Fatal("malformed OTLP endpoint unexpectedly succeeded")
	}
}

func TestMetricCardinalityIsBounded(t *testing.T) {
	t.Parallel()
	metrics := NewMetrics()
	for index := 0; index < 10_000; index++ {
		value := strings.Repeat("x", index%100) + time.Duration(index).String()
		metrics.ProviderOperation(value, value, value, time.Millisecond)
		metrics.HTTP(value, "/capsules/"+value, 700+index, time.Millisecond)
		metrics.RunTransition(value, value)
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if len(metrics.counters) != 2 || len(metrics.counts) != 2 {
		t.Fatalf("dynamic inputs increased cardinality: counters=%d observations=%d", len(metrics.counters), len(metrics.counts))
	}
}
