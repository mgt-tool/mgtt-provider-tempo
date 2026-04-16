package probes

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mgt-tool/mgtt-provider-tempo/internal/tempoclient"
	"github.com/mgt-tool/mgtt/sdk/provider"
)

// span_invariant facts query Tempo's TraceQL Metrics endpoint
// (/api/metrics/query_range). Each fact maps to one TraceQL query with a
// metrics function; the result is a Prometheus-style time series.

const window5m = 5 * time.Minute

func registerSpanInvariant(r *provider.Registry) {
	r.Register("span_invariant", map[string]provider.ProbeFn{
		"current_p99": percentile(99),
		"current_p95": percentile(95),
		"current_p50": percentile(50),

		"request_count_5m": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			tf, span, err := setup(req)
			if err != nil {
				return provider.Result{}, err
			}
			q := fmt.Sprintf(`%s | count_over_time()`, matcherBlock(span, req.Extra["span_filter"], ""))
			res, err := tf.QueryRangeMetrics(ctx, q, window5m)
			if err != nil {
				return provider.Result{}, err
			}
			return provider.IntResult(int(res.SumLatest())), nil
		},

		"error_rate_5m": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			tf, span, err := setup(req)
			if err != nil {
				return provider.Result{}, err
			}
			filter := req.Extra["span_filter"]
			errQ := fmt.Sprintf(`%s | count_over_time()`, matcherBlock(span, filter, "status = error"))
			totalQ := fmt.Sprintf(`%s | count_over_time()`, matcherBlock(span, filter, ""))
			errRes, err := tf.QueryRangeMetrics(ctx, errQ, window5m)
			if err != nil {
				return provider.Result{}, err
			}
			totalRes, err := tf.QueryRangeMetrics(ctx, totalQ, window5m)
			if err != nil {
				return provider.Result{}, err
			}
			total := totalRes.SumLatest()
			if total == 0 {
				return provider.FloatResult(0), nil
			}
			return provider.FloatResult(errRes.SumLatest() / total), nil
		},

		"breach_duration": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			tf, span, err := setup(req)
			if err != nil {
				return provider.Result{}, err
			}
			targetRaw, err := resolveTargetMax(req)
			if err != nil {
				return provider.Result{}, err
			}
			target, err := time.ParseDuration(targetRaw)
			if err != nil {
				return provider.Result{}, fmt.Errorf("%w: target_max %q is not a duration: %v",
					provider.ErrUsage, targetRaw, err)
			}
			q := fmt.Sprintf(`%s | quantile_over_time(duration, 0.99)`,
				matcherBlock(span, req.Extra["span_filter"], ""))
			res, err := tf.QueryRangeMetrics(ctx, q, 30*time.Minute)
			if err != nil {
				return provider.Result{}, err
			}
			return provider.IntResult(trailingBreachSeconds(res, target.Seconds()*1e9)), nil
		},
	})
}

// percentile returns a ProbeFn that runs `quantile_over_time(.<n>, duration)`
// against the supplied span and returns the latest value in milliseconds.
func percentile(p int) provider.ProbeFn {
	return func(ctx context.Context, req provider.Request) (provider.Result, error) {
		tf, span, err := setup(req)
		if err != nil {
			return provider.Result{}, err
		}
		q := fmt.Sprintf(`%s | quantile_over_time(duration, 0.%02d)`,
			matcherBlock(span, req.Extra["span_filter"], ""), p)
		res, err := tf.QueryRangeMetrics(ctx, q, window5m)
		if err != nil {
			return provider.Result{}, err
		}
		v, ok := res.LatestValue()
		if !ok {
			return provider.FloatResult(0), nil
		}
		return provider.FloatResult(v / 1e6), nil
	}
}

// matcherBlock builds a TraceQL `{ ... }` block from a span name plus
// optional caller-supplied attribute filter and optional extra clause
// (used for the per-status sub-query in error_rate_5m). Clauses are joined
// with `&&`. Examples:
//
//	matcherBlock("checkout.init", "", "")                              → { name = "checkout.init" }
//	matcherBlock("checkout.init", "", "status = error")                → { name = "checkout.init" && status = error }
//	matcherBlock("checkout.init", `resource.color = "green"`, "")      → { name = "checkout.init" && resource.color = "green" }
func matcherBlock(name, filter, extra string) string {
	parts := []string{fmt.Sprintf("name = %q", name)}
	if filter = strings.TrimSpace(filter); filter != "" {
		parts = append(parts, filter)
	}
	if extra = strings.TrimSpace(extra); extra != "" {
		parts = append(parts, extra)
	}
	return "{ " + strings.Join(parts, " && ") + " }"
}

// setup pulls the required extras and constructs the client. Returns the
// client and the span name in one call to keep probe bodies short.
func setup(req provider.Request) (*tempoclient.Client, string, error) {
	span, err := resolveSpan(req)
	if err != nil {
		return nil, "", err
	}
	c, err := newClient(req)
	if err != nil {
		return nil, "", err
	}
	return c, span, nil
}

// trailingBreachSeconds walks the time series from newest to oldest and
// counts how many consecutive trailing samples exceed the threshold (in ns).
// Returns the duration in seconds.
func trailingBreachSeconds(res *tempoclient.MetricsResponse, thresholdNs float64) int {
	if len(res.Series) == 0 || len(res.Series[0].Samples) == 0 {
		return 0
	}
	samples := res.Series[0].Samples
	count := 0
	for i := len(samples) - 1; i >= 0; i-- {
		if samples[i].Value <= thresholdNs {
			break
		}
		count++
	}
	// Each step is 30s (set in QueryRangeMetrics).
	return count * 30
}
