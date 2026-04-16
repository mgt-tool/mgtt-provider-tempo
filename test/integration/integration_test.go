//go:build integration

// Package integration exercises mgtt-provider-tempo end-to-end against a
// real Tempo instance. Tempo runs in docker (single-binary mode); spans
// are pushed via OTLP/HTTP in JSON form. The provider binary is built
// fresh each run.
//
// Run with:
//
//	go test -tags=integration ./test/integration/...
//
// Requirements on the host: docker, go. Tests are skipped when docker is
// unavailable.
package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	containerName = "mgtt-provider-tempo-it"
	tempoImage    = "grafana/tempo:2.6.0"
	tempoHTTPPort = "3200"
	tempoOTLPPort = "4318"
)

// ---------------------------------------------------------------------------
// Test lifecycle
// ---------------------------------------------------------------------------

var tempoBaseURL = "http://localhost:" + tempoHTTPPort

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Fprintln(os.Stderr, "docker not on PATH; skipping tempo integration tests")
		os.Exit(0)
	}
	if err := ensureTempo(); err != nil {
		panic("ensureTempo: " + err.Error())
	}
	if err := waitForReady(2 * time.Minute); err != nil {
		panic("waitForReady: " + err.Error())
	}
	code := m.Run()
	// Container is preserved across runs for iteration speed; destroy with:
	//   docker rm -f mgtt-provider-tempo-it
	os.Exit(code)
}

func ensureTempo() error {
	// Already running?
	out, _ := exec.Command("docker", "ps", "--filter", "name=^"+containerName+"$",
		"--format", "{{.Names}}").Output()
	if strings.Contains(string(out), containerName) {
		return nil
	}
	// Stopped but exists? Remove it.
	exec.Command("docker", "rm", "-f", containerName).Run()

	cfgPath, err := filepath.Abs(filepath.Join("testdata", "tempo.yaml"))
	if err != nil {
		return err
	}
	cmd := exec.Command("docker", "run", "-d",
		"--name", containerName,
		"-p", tempoHTTPPort+":3200",
		"-p", tempoOTLPPort+":4318",
		"-v", cfgPath+":/etc/tempo.yaml:ro",
		tempoImage,
		"-config.file=/etc/tempo.yaml",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func waitForReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(tempoBaseURL + "/ready")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				// One more pause — /ready returns OK before the OTLP
				// receiver finishes binding. 1s is plenty.
				time.Sleep(1 * time.Second)
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("tempo did not become ready within %s", timeout)
}

// ---------------------------------------------------------------------------
// OTLP/HTTP span emitter
// ---------------------------------------------------------------------------

// pushSpans emits n spans with the given name and duration, status set per
// `status` (0=unset, 1=ok, 2=error). Spans are batched in a single OTLP
// request so ingest is one round-trip per call.
func pushSpans(t *testing.T, name string, n int, duration time.Duration, status int) {
	t.Helper()
	now := time.Now().UnixNano()
	spans := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		traceID := randomHex(16) // 32 hex chars
		spanID := randomHex(8)   // 16 hex chars
		span := map[string]any{
			"traceId":           traceID,
			"spanId":            spanID,
			"name":              name,
			"kind":              2, // SERVER
			"startTimeUnixNano": fmt.Sprintf("%d", now-duration.Nanoseconds()),
			"endTimeUnixNano":   fmt.Sprintf("%d", now),
		}
		if status != 0 {
			span["status"] = map[string]any{"code": status}
		}
		spans = append(spans, span)
	}
	payload := map[string]any{
		"resourceSpans": []any{
			map[string]any{
				"resource": map[string]any{
					"attributes": []any{
						map[string]any{
							"key":   "service.name",
							"value": map[string]any{"stringValue": "integration-test"},
						},
					},
				},
				"scopeSpans": []any{
					map[string]any{"spans": spans},
				},
			},
		},
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(
		"http://localhost:"+tempoOTLPPort+"/v1/traces",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("push spans: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("push spans: HTTP %d: %s", resp.StatusCode, out)
	}
	// Tempo flushes ingested spans on a short interval; give it a beat
	// so the metrics endpoint sees them.
	time.Sleep(3 * time.Second)
}

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Provider binary harness
// ---------------------------------------------------------------------------

func buildProviderBinary(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "mgtt-provider-tempo")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build provider: %v\n%s", err, out)
	}
	return bin
}

type probeResult struct {
	Value  any    `json:"value"`
	Raw    string `json:"raw"`
	Status string `json:"status"`
}

func probe(t *testing.T, binary, fact string, extras ...string) probeResult {
	t.Helper()
	args := []string{"probe", "test_invariant", fact, "--type", "span_invariant"}
	args = append(args, extras...)
	cmd := exec.Command(binary, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe %s extras=%v: %v\nstderr: %s", fact, extras, err, stderr.String())
	}
	var r probeResult
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("decode probe output: %v (raw=%q)", err, out)
	}
	return r
}

func probeAllowFail(t *testing.T, binary string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run provider: %v", err)
	}
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), code
}

func extras(span string) []string {
	return []string{
		"--tempo_url", tempoBaseURL,
		"--span", span,
		"--target_max", "100ms",
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// uniqueSpanName returns a span name unique to this test run so concurrent
// tests don't see each other's spans (Tempo container is shared).
func uniqueSpanName(t *testing.T, prefix string) string {
	t.Helper()
	return prefix + "." + randomHex(4)
}

// ---------------------------------------------------------------------------
// Scenario 1 — healthy span: well under target_max, no errors
// ---------------------------------------------------------------------------

func TestScenario_HealthySpan(t *testing.T) {
	span := uniqueSpanName(t, "healthy_checkout")
	// 50 spans @ 10ms each — well under our 100ms target.
	pushSpans(t, span, 50, 10*time.Millisecond, 1)

	binary := buildProviderBinary(t)
	args := []string{
		"--tempo_url", tempoBaseURL,
		"--span", span,
		"--target_max", "100ms",
	}

	t.Run("request_count_5m sees the spans", func(t *testing.T) {
		r := probe(t, binary, "request_count_5m", args...)
		v, _ := r.Value.(float64)
		if int(v) < 50 {
			t.Fatalf("want >= 50 spans, got %v", r.Value)
		}
	})

	t.Run("current_p99 is under target_max", func(t *testing.T) {
		r := probe(t, binary, "current_p99", args...)
		v, _ := r.Value.(float64)
		if v >= 100 {
			t.Fatalf("p99 should be under 100ms target, got %vms", v)
		}
	})

	t.Run("error_rate_5m is zero", func(t *testing.T) {
		r := probe(t, binary, "error_rate_5m", args...)
		if v, _ := r.Value.(float64); v != 0 {
			t.Fatalf("no error spans pushed; want 0, got %v", v)
		}
	})

	t.Run("breach_duration is zero", func(t *testing.T) {
		r := probe(t, binary, "breach_duration", args...)
		if v, _ := r.Value.(float64); v != 0 {
			t.Fatalf("no breach; want 0, got %v", v)
		}
	})
}

// ---------------------------------------------------------------------------
// Scenario 2 — error rate non-zero
// ---------------------------------------------------------------------------

func TestScenario_ErrorRate(t *testing.T) {
	span := uniqueSpanName(t, "errorful_op")
	// 30 ok + 20 error → expected error_rate ~= 0.4.
	pushSpans(t, span, 30, 10*time.Millisecond, 1)
	pushSpans(t, span, 20, 10*time.Millisecond, 2)

	binary := buildProviderBinary(t)
	r := probe(t, binary, "error_rate_5m",
		"--tempo_url", tempoBaseURL,
		"--span", span,
		"--target_max", "1s",
	)
	v, _ := r.Value.(float64)
	// Allow a wide band — Tempo metrics aggregation has its own rounding.
	if v < 0.30 || v > 0.50 {
		t.Fatalf("expected error rate around 0.4, got %v", v)
	}
}

// ---------------------------------------------------------------------------
// Scenario 3 — no data: querying an unknown span name
// ---------------------------------------------------------------------------

func TestScenario_NoData(t *testing.T) {
	binary := buildProviderBinary(t)
	r := probe(t, binary, "current_p99",
		"--tempo_url", tempoBaseURL,
		"--span", "span.that.was.never.emitted."+randomHex(4),
		"--target_max", "1s",
	)
	if v, _ := r.Value.(float64); v != 0 {
		t.Fatalf("missing span should yield p99=0, got %v", v)
	}
}

// ---------------------------------------------------------------------------
// Scenario 4 — usage errors must surface as exit 1
// ---------------------------------------------------------------------------

func TestScenario_MissingSpanFlag_ErrUsage(t *testing.T) {
	binary := buildProviderBinary(t)
	_, stderr, code := probeAllowFail(t, binary,
		"probe", "x", "current_p99",
		"--type", "span_invariant",
		"--tempo_url", tempoBaseURL,
		"--target_max", "1s",
	)
	if code != 1 {
		t.Fatalf("missing --span: want exit 1, got %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--span") {
		t.Fatalf("error message should mention --span: %s", stderr)
	}
}

func TestScenario_MissingTempoURL_ErrUsage(t *testing.T) {
	binary := buildProviderBinary(t)
	_, stderr, code := probeAllowFail(t, binary,
		"probe", "x", "current_p99",
		"--type", "span_invariant",
		"--span", "irrelevant",
		"--target_max", "1s",
	)
	if code != 1 {
		t.Fatalf("missing --tempo_url: want exit 1, got %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--tempo_url") {
		t.Fatalf("error message should mention --tempo_url: %s", stderr)
	}
}

// ---------------------------------------------------------------------------
// Scenario 5 — auth failure (Tempo isn't actually behind auth in our test
// container, so we point at a nonexistent host to force a transient class
// error — proves the classifier works end-to-end via the binary).
// ---------------------------------------------------------------------------

func TestScenario_UnreachableTempo_ErrTransient(t *testing.T) {
	binary := buildProviderBinary(t)
	// Use a deliberately-bad URL; expect non-zero exit (transient = 4).
	_, stderr, code := probeAllowFail(t, binary,
		"probe", "x", "current_p99",
		"--type", "span_invariant",
		"--tempo_url", "http://localhost:1",
		"--span", "irrelevant",
		"--target_max", "1s",
	)
	if code != 4 {
		t.Fatalf("unreachable tempo: want exit 4 (transient), got %d stderr=%s", code, stderr)
	}
}

var _ = context.Background // keep imports
