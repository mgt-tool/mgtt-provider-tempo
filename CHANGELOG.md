# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versioning: [SemVer](https://semver.org/).

## [1.0.1] — 2026-04-18

### Changed

- `manifest.yaml` backend range separator normalized from comma (`">=2.6.0,<2.7.0"`) to space (`">=2.6.0 <2.7.0"`) — matches the semver ecosystem convention used by npm, cargo, bundler, and the mgtt v1.0 schema spec example. Semantics unchanged.

## [1.0.0] — 2026-04-18

### Changed (breaking)

- `manifest.yaml` migrated to the v1.0 mgtt schema: three top-level blocks (`meta`, `runtime`, `install`); `hooks:` retired; `needs:` + `network:` moved under `runtime:`; install methods declared via `install.source` + `install.image` subblocks. Requires mgtt ≥ 0.2.0.

## [0.2.0] — 2026-04-16

### Added

- **`compatibility:` block in `manifest.yaml`** — declares the backend versions this provider is built against (Tempo `2.6.x`), the exact image digests the integration tests run against, and the version-sensitive behaviors callers should know about (response shape and TraceQL syntax both shifted vs. 2.5). README surfaces the contract prominently near the top.
- **`span_filter` var on `tracing.span_invariant`** — appends a TraceQL attribute matcher to every query. Lets one component scope to one blue/green color, one route, one tenant, etc. Example: `span_filter: 'resource.deployment.color = "{color}"'`.
- **`post_switch_canary` SLO** in the Magento example — uses `span_filter` to bind a tighter error budget to just the just-promoted color, depending on a `kubernetes.service` so a stuck switch surfaces both at the kubernetes layer and the latency layer.
- **Per-component SLO targets** (`target_max_error_rate`, `breach_tolerance_seconds`) — type manifest's `healthy:` and `states:` reference these vars instead of hardcoded constants. Each component reads as a three-number contract.
- **Integration test image pinned by digest** — `grafana/tempo:2.6.0@sha256:f55a8a19…7691e822`. Future tag rollovers can no longer silently break the test suite.

### Fixed

- **TraceQL Metrics response parser updated for Tempo 2.6** — was Prometheus-shaped (`{"data":{"result":[]}}`), now native (`{"series":[{"labels":[],"samples":[…]}]}`). The old parser silently decoded the new shape to empty, so probes returned 0 against Tempo 2.6 deployments.
- **Percentile syntax updated for Tempo 2.6** — `quantile_over_time(.99, duration)` → `quantile_over_time(duration, 0.99)`. The old form returns HTTP 500 from 2.6+.

### Known issues

- **Happy-path integration scenarios skip by default.** They depend on Tempo's TraceQL Metrics endpoint returning real samples, which requires the metrics_generator's `local-blocks` processor + correct distributor → metrics_generator routing — a deeper Tempo configuration story than this release sorted out. Set `MGTT_TEMPO_VERIFIED_METRICS=1` to enable them when running against a Tempo backend you've already verified end-to-end. Negative-path scenarios (missing-flag, unreachable, no-data) run by default.

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
