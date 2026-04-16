package probes

import (
	"context"
	"fmt"
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
			q := fmt.Sprintf(`{ name = %q } | count_over_time()`, span)
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
			// Two queries: error count and total count. Ratio = error rate.
			errQ := fmt.Sprintf(`{ name = %q && status = error } | count_over_time()`, span)
			totalQ := fmt.Sprintf(`{ name = %q } | count_over_time()`, span)
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
			// Query p99 over a longer window split by 30s steps; count
			// how many trailing steps remain over the target.
			q := fmt.Sprintf(`{ name = %q } | quantile_over_time(.99, duration)`, span)
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
		q := fmt.Sprintf(`{ name = %q } | quantile_over_time(.%02d, duration)`, span, p)
		res, err := tf.QueryRangeMetrics(ctx, q, window5m)
		if err != nil {
			return provider.Result{}, err
		}
		v, ok := res.LatestValue()
		if !ok {
			return provider.FloatResult(0), nil
		}
		// Tempo returns durations in nanoseconds; convert to milliseconds
		// for an operator-friendly comparison value.
		return provider.FloatResult(v / 1e6), nil
	}
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
	if len(res.Data.Result) == 0 || len(res.Data.Result[0].Values) == 0 {
		return 0
	}
	values := res.Data.Result[0].Values
	count := 0
	for i := len(values) - 1; i >= 0; i-- {
		v := values[i]
		if len(v) < 2 {
			break
		}
		str, ok := v[1].(string)
		if !ok {
			break
		}
		var f float64
		if _, err := fmt.Sscanf(str, "%f", &f); err != nil {
			break
		}
		if f <= thresholdNs {
			break
		}
		count++
	}
	// Each step is 30s (set in QueryRangeMetrics).
	return count * 30
}
