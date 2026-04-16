package probes

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mgt-tool/mgtt-provider-tempo/internal/tempoclient"
	"github.com/mgt-tool/mgtt/sdk/provider"
)

// fakeTempo replaces NewTempoConstructor for the duration of one test.
func fakeTempo(t *testing.T, body string, status int) {
	t.Helper()
	prev := NewTempoConstructor
	t.Cleanup(func() { NewTempoConstructor = prev })
	NewTempoConstructor = func(_, _, _ string) *tempoclient.Client {
		c := tempoclient.New("http://stub", "", "")
		c.Do = func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}
		return c
	}
}

func runProbe(t *testing.T, fact string, extra map[string]string) provider.Result {
	t.Helper()
	r := provider.NewRegistry()
	Register(r)
	res, err := r.Probe(context.Background(), provider.Request{
		Type: "span_invariant", Name: "checkout", Fact: fact,
		Extra: extra,
	})
	if err != nil {
		t.Fatalf("probe %s: %v", fact, err)
	}
	return res
}

func runProbeErr(t *testing.T, fact string, extra map[string]string) error {
	t.Helper()
	r := provider.NewRegistry()
	Register(r)
	_, err := r.Probe(context.Background(), provider.Request{
		Type: "span_invariant", Name: "checkout", Fact: fact,
		Extra: extra,
	})
	return err
}

func extras(span string) map[string]string {
	return map[string]string{
		"tempo_url":  "http://stub",
		"span":       span,
		"target_max": "1s",
	}
}

func TestCurrentP99_ReturnsMillisecondsFromNanoseconds(t *testing.T) {
	// 1.5 ms in ns = 1500000
	body := `{"data":{"resultType":"matrix","result":[
		{"metric":{},"values":[[1700000000,"1500000"]]}
	]}}`
	fakeTempo(t, body, 200)

	res := runProbe(t, "current_p99", extras("checkout.init"))
	v, _ := res.Value.(float64)
	if v != 1.5 {
		t.Fatalf("expected 1.5ms (1500000ns / 1e6), got %v", res.Value)
	}
}

func TestCurrentP99_NoData_ReturnsZero(t *testing.T) {
	body := `{"data":{"resultType":"matrix","result":[]}}`
	fakeTempo(t, body, 200)
	res := runProbe(t, "current_p99", extras("checkout.init"))
	if v, _ := res.Value.(float64); v != 0 {
		t.Fatalf("empty result must yield 0, got %v", res.Value)
	}
}

func TestRequestCount5m_SumsAcrossSeries(t *testing.T) {
	body := `{"data":{"result":[
		{"values":[[1,"10"]]},
		{"values":[[1,"15"]]}
	]}}`
	fakeTempo(t, body, 200)
	res := runProbe(t, "request_count_5m", extras("checkout.init"))
	if v, _ := res.Value.(int); v != 25 {
		t.Fatalf("want 25, got %v", res.Value)
	}
}

func TestErrorRate5m_HappyPath(t *testing.T) {
	// First call returns errors=2, second returns total=10.
	// The fake serves the same body for both — fine for this assertion
	// because we just need a valid ratio shape.
	body := `{"data":{"result":[{"values":[[1,"5"]]}]}}`
	fakeTempo(t, body, 200)
	res := runProbe(t, "error_rate_5m", extras("checkout.init"))
	if v, _ := res.Value.(float64); v != 1.0 {
		t.Fatalf("ratio of equal-value queries should be 1.0, got %v", res.Value)
	}
}

func TestErrorRate5m_ZeroTotalReturnsZero(t *testing.T) {
	body := `{"data":{"result":[]}}`
	fakeTempo(t, body, 200)
	res := runProbe(t, "error_rate_5m", extras("checkout.init"))
	if v, _ := res.Value.(float64); v != 0 {
		t.Fatalf("zero total → 0, got %v", res.Value)
	}
}

func TestBreachDuration_NoBreach(t *testing.T) {
	// All values well under 1s threshold (1e9 ns).
	body := `{"data":{"result":[{"values":[
		[1,"100000"],[2,"110000"],[3,"95000"]
	]}]}}`
	fakeTempo(t, body, 200)
	res := runProbe(t, "breach_duration", extras("checkout.init"))
	if v, _ := res.Value.(int); v != 0 {
		t.Fatalf("no breach → 0, got %v", res.Value)
	}
}

func TestBreachDuration_TrailingBreach(t *testing.T) {
	// Last 3 samples are over 1s (1e9 ns); 30s step → 90s breach.
	body := `{"data":{"result":[{"values":[
		[1,"100000"],[2,"110000"],[3,"1500000000"],[4,"1700000000"],[5,"2000000000"]
	]}]}}`
	fakeTempo(t, body, 200)
	res := runProbe(t, "breach_duration", extras("checkout.init"))
	if v, _ := res.Value.(int); v != 90 {
		t.Fatalf("want 90s (3 trailing samples × 30s step), got %v", res.Value)
	}
}

func TestMissingSpanFlag_ErrUsage(t *testing.T) {
	// No `span` extra.
	err := runProbeErr(t, "current_p99", map[string]string{
		"tempo_url":  "http://stub",
		"target_max": "1s",
	})
	if !errors.Is(err, provider.ErrUsage) {
		t.Fatalf("missing --span must be ErrUsage, got %v", err)
	}
}

func TestMissingTempoURL_ErrUsage(t *testing.T) {
	err := runProbeErr(t, "current_p99", map[string]string{
		"span":       "x",
		"target_max": "1s",
	})
	if !errors.Is(err, provider.ErrUsage) {
		t.Fatalf("missing --tempo_url must be ErrUsage, got %v", err)
	}
}

func TestBreachDuration_BadTargetMax_ErrUsage(t *testing.T) {
	err := runProbeErr(t, "breach_duration", map[string]string{
		"tempo_url":  "http://stub",
		"span":       "x",
		"target_max": "garbage",
	})
	if !errors.Is(err, provider.ErrUsage) {
		t.Fatalf("bad target_max must be ErrUsage, got %v", err)
	}
}

func TestRegistryWiresSpanInvariant(t *testing.T) {
	r := provider.NewRegistry()
	Register(r)
	for _, want := range []string{"current_p99", "current_p95", "current_p50",
		"request_count_5m", "error_rate_5m", "breach_duration"} {
		found := false
		for _, f := range r.Facts("span_invariant") {
			if f == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("registry missing span_invariant fact %q", want)
		}
	}
}
