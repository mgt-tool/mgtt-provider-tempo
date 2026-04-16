# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versioning: [SemVer](https://semver.org/).

## [0.1.0] — 2026-04-16

Initial release. Per-span SLO checks against Grafana Tempo's TraceQL Metrics endpoint.

### Added

- **`tracing.span_invariant` type** with six facts:
  - `current_p99`, `current_p95`, `current_p50` — percentiles in milliseconds
  - `request_count_5m` — denominator context
  - `error_rate_5m` — fraction of spans with `status=error`
  - `breach_duration` — trailing seconds over `target_max`
- **`internal/tempoclient/`** — HTTP client with timeout, auth/tenant headers, and Tempo status code → sentinel error mapping (401/403→Forbidden, 404→NotFound, 400→Usage, 5xx→Transient).
- **Example model** in `examples/magento-platform.model.yaml` — four customer-facing SLOs wired alongside kubernetes/aws infra.
- **README "Emitting spans to Tempo"** section with PHP/Magento bootstrap, env vars, and a `curl` to verify spans arrive before debugging the model.
