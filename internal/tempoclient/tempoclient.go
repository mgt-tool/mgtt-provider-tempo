// Package tempoclient wraps Tempo's HTTP API with timeout, auth, and the
// classify-to-sentinel-error mapping used by every tempo-provider probe.
package tempoclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mgt-tool/mgtt/sdk/provider"
)

const (
	defaultTimeout = 30 * time.Second
	maxBytes       = 10 * 1024 * 1024
)

// Client is a thin Tempo HTTP wrapper. Tests inject Do for fakes.
type Client struct {
	BaseURL  string
	Token    string
	TenantID string
	Timeout  time.Duration
	Do       func(req *http.Request) (*http.Response, error)
}

// New returns a Client with sensible defaults.
func New(baseURL, token, tenantID string) *Client {
	return &Client{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		Token:    token,
		TenantID: tenantID,
		Timeout:  defaultTimeout,
		Do:       (&http.Client{Timeout: defaultTimeout}).Do,
	}
}

// QueryRangeMetrics calls Tempo's TraceQL Metrics endpoint. q is a TraceQL
// expression with a metrics function (rate, count_over_time,
// histogram_over_time, etc.). Returns the parsed Prometheus-shaped JSON.
func (c *Client) QueryRangeMetrics(ctx context.Context, q string, since time.Duration) (*MetricsResponse, error) {
	end := time.Now()
	start := end.Add(-since)
	u, err := url.Parse(c.BaseURL + "/api/metrics/query_range")
	if err != nil {
		return nil, fmt.Errorf("%w: parse tempo url: %v", provider.ErrEnv, err)
	}
	qs := u.Query()
	qs.Set("q", q)
	qs.Set("start", fmt.Sprintf("%d", start.Unix()))
	qs.Set("end", fmt.Sprintf("%d", end.Unix()))
	qs.Set("step", "30s")
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", provider.ErrEnv, err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.TenantID != "" {
		req.Header.Set("X-Scope-OrgID", c.TenantID)
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, classifyTransport(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: read tempo response: %v", provider.ErrTransient, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTP(resp.StatusCode, body)
	}
	var out MetricsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: parse tempo json: %v", provider.ErrProtocol, err)
	}
	return &out, nil
}

// MetricsResponse mirrors Tempo's Prometheus-style response shape.
type MetricsResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string         `json:"resultType"`
		Result     []SeriesResult `json:"result"`
	} `json:"data"`
}

// SeriesResult is one labeled time series.
type SeriesResult struct {
	Metric map[string]string `json:"metric"`
	Values [][]any           `json:"values"` // [[timestamp, "value"], ...]
}

// LatestValue returns the most recent value of the first matching series,
// or (0, false) when there are no points. Tempo represents values as
// strings; we parse to float64.
func (r *MetricsResponse) LatestValue() (float64, bool) {
	for _, s := range r.Data.Result {
		if len(s.Values) == 0 {
			continue
		}
		last := s.Values[len(s.Values)-1]
		if len(last) < 2 {
			continue
		}
		if str, ok := last[1].(string); ok {
			var f float64
			if _, err := fmt.Sscanf(str, "%f", &f); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

// SumLatest sums the last value across all series — useful for `count` and
// `rate` queries that may split by labels.
func (r *MetricsResponse) SumLatest() float64 {
	sum := 0.0
	for _, s := range r.Data.Result {
		if len(s.Values) == 0 {
			continue
		}
		last := s.Values[len(s.Values)-1]
		if len(last) < 2 {
			continue
		}
		if str, ok := last[1].(string); ok {
			var f float64
			if _, err := fmt.Sscanf(str, "%f", &f); err == nil {
				sum += f
			}
		}
	}
	return sum
}

// classifyTransport maps net errors to sentinel errors.
func classifyTransport(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no such host"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "context deadline exceeded"),
		strings.Contains(msg, "TLS handshake timeout"):
		return fmt.Errorf("%w: %s", provider.ErrTransient, msg)
	}
	return fmt.Errorf("%w: %s", provider.ErrEnv, msg)
}

// classifyHTTP maps Tempo HTTP error codes to sentinel errors.
func classifyHTTP(status int, body []byte) error {
	first := firstLine(string(body))
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w: tempo HTTP %d: %s", provider.ErrForbidden, status, first)
	case status == http.StatusNotFound:
		return fmt.Errorf("%w: tempo HTTP %d: %s", provider.ErrNotFound, status, first)
	case status == http.StatusBadRequest:
		// Bad TraceQL — caller bug, surface as usage error.
		return fmt.Errorf("%w: tempo rejected query: %s", provider.ErrUsage, first)
	case status >= 500 && status < 600:
		return fmt.Errorf("%w: tempo HTTP %d: %s", provider.ErrTransient, status, first)
	}
	return fmt.Errorf("%w: tempo HTTP %d: %s", provider.ErrEnv, status, first)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
