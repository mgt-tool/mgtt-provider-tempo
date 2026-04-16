package tempoclient

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mgt-tool/mgtt/sdk/provider"
)

// fakeTransport is an http.Client.Do replacement.
type fakeRT struct {
	status int
	body   string
	err    error
}

func (f fakeRT) do(req *http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
	}, nil
}

func TestQueryRangeMetrics_HappyPath(t *testing.T) {
	c := New("http://tempo:3200", "", "")
	c.Do = fakeRT{
		status: 200,
		body: `{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{},"values":[[1700000000,"1500000"],[1700000030,"1700000"]]}
		]}}`,
	}.do
	res, err := c.QueryRangeMetrics(t.Context(), `{ name = "x" } | quantile_over_time(.99, duration)`, 5*60)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := res.LatestValue()
	if !ok {
		t.Fatal("expected a value")
	}
	if v != 1700000 {
		t.Fatalf("want 1700000 (latest), got %v", v)
	}
}

func TestQueryRangeMetrics_HTTPCodes(t *testing.T) {
	cases := map[int]error{
		401: provider.ErrForbidden,
		403: provider.ErrForbidden,
		404: provider.ErrNotFound,
		400: provider.ErrUsage,
		500: provider.ErrTransient,
		503: provider.ErrTransient,
	}
	for code, want := range cases {
		c := New("http://tempo:3200", "", "")
		c.Do = fakeRT{status: code, body: "error body"}.do
		_, err := c.QueryRangeMetrics(t.Context(), "{}", 60)
		if !errors.Is(err, want) {
			t.Errorf("HTTP %d: want %v, got %v", code, want, err)
		}
	}
}

func TestQueryRangeMetrics_TransportErrors(t *testing.T) {
	cases := []string{
		"dial tcp: lookup tempo: no such host",
		"connection refused",
		"context deadline exceeded",
		"TLS handshake timeout",
	}
	for _, msg := range cases {
		c := New("http://tempo:3200", "", "")
		c.Do = fakeRT{err: errors.New(msg)}.do
		_, err := c.QueryRangeMetrics(t.Context(), "{}", 60)
		if !errors.Is(err, provider.ErrTransient) {
			t.Errorf("transport %q: want ErrTransient, got %v", msg, err)
		}
	}
}

func TestQueryRangeMetrics_BadJSON(t *testing.T) {
	c := New("http://tempo:3200", "", "")
	c.Do = fakeRT{status: 200, body: "not json"}.do
	_, err := c.QueryRangeMetrics(t.Context(), "{}", 60)
	if !errors.Is(err, provider.ErrProtocol) {
		t.Fatalf("want ErrProtocol, got %v", err)
	}
}

func TestSumLatest(t *testing.T) {
	r := &MetricsResponse{}
	r.Data.Result = []SeriesResult{
		{Values: [][]any{{1.0, "10"}, {2.0, "12"}}},
		{Values: [][]any{{1.0, "5"}, {2.0, "8"}}},
	}
	if got := r.SumLatest(); got != 20 {
		t.Fatalf("want 20 (12+8), got %v", got)
	}
}

func TestSumLatest_EmptyIsZero(t *testing.T) {
	r := &MetricsResponse{}
	if got := r.SumLatest(); got != 0 {
		t.Fatalf("want 0, got %v", got)
	}
}

func TestAuthAndTenantHeaders(t *testing.T) {
	var seenAuth, seenTenant string
	c := New("http://tempo:3200", "secret-token", "general")
	c.Do = func(req *http.Request) (*http.Response, error) {
		seenAuth = req.Header.Get("Authorization")
		seenTenant = req.Header.Get("X-Scope-OrgID")
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"data":{"result":[]}}`)),
		}, nil
	}
	_, _ = c.QueryRangeMetrics(t.Context(), "{}", 60)
	if seenAuth != "Bearer secret-token" {
		t.Errorf("auth header missing or wrong: %q", seenAuth)
	}
	if seenTenant != "general" {
		t.Errorf("tenant header missing: %q", seenTenant)
	}
}
