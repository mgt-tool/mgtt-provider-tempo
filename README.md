# mgtt-provider-tempo

Per-span SLO checks for [mgtt](https://github.com/mgt-tool/mgtt) backed by Grafana Tempo.

```yaml
checkout_init_p95:
  type: tracing.span_invariant
  providers: [tempo]
  vars:
    tempo_url: https://tempo.observability.svc:3200
    span: "checkout.init"
    target_max: 800ms
```

When `mgtt plan` walks this component, it asks Tempo: "what's the p99 of `checkout.init` right now, and how long has it been over 800ms?" — using the answer to drive root-cause reasoning.

## Compatibility

| | |
|---|---|
| **Backend** | Grafana Tempo |
| **Versions** | `2.6.x` |
| **Tested against** | `grafana/tempo:2.6.0` (digest pinned in integration tests) |

Tempo's TraceQL Metrics response shape and TraceQL syntax both shifted between minor versions — earlier (`<2.6`) and later (`>=2.7`) deployments will silently return zeros or 4xx the queries. See [`provider.yaml`](./provider.yaml#L19) for the full contract.

## Install

Two equivalent paths — pick whichever fits your workflow:

```bash
# Git + host toolchain (requires Go 1.25+)
mgtt provider install tempo

# Pre-built Docker image (no local toolchain, digest-pinned)
mgtt provider install --image ghcr.io/mgt-tool/mgtt-provider-tempo:0.2.0@sha256:...
```

The image is published by [this repo's CI](./.github/workflows/docker.yml) on every push to `main` and every `v*` tag. Find the current digest on the [GHCR package page](https://github.com/mgt-tool/mgtt-provider-tempo/pkgs/container/mgtt-provider-tempo).

## Capabilities

When installed as an image, this provider declares the following runtime capabilities in [`provider.yaml`](./provider.yaml) (top-level `needs:`):

| Capability | Effect at probe time |
|---|---|
| `network` | `--network host` — container reaches the Tempo HTTP URL you configure via `vars.tempo_url` |

No host credentials are forwarded; Tempo auth (when applicable) is passed per-component via the `auth_token` and `tenant_id` model vars.

Operators can override or extend the vocabulary via `$MGTT_HOME/capabilities.yaml`, and refuse specific caps via `MGTT_IMAGE_CAPS_DENY=...`. See the [full capabilities reference](https://github.com/mgt-tool/mgtt/blob/main/docs/reference/image-capabilities.md). Git-installed invocations don't go through this layer — the binary runs with the operator's full environment.

## Auth

| Variable | Purpose | Required |
|---|---|---|
| `tempo_url` | Base URL of the Tempo HTTP API | yes |
| `auth_token` | Bearer token (when Tempo is fronted by auth) | no |
| `tenant_id` | `X-Scope-OrgID` for multi-tenant Tempo | no |
| `span_filter` | TraceQL attribute matcher appended to every query (e.g. `resource.deployment.color = "green"`) | no |

Probes are HTTP `GET` only; the provider never writes to Tempo.

## Type: `tracing.span_invariant`

One per named span you have an SLO on.

| Fact | Type | Returns |
|---|---|---|
| `current_p99` | float (ms) | p99 duration of `<span>` over the last 5 minutes |
| `current_p95` | float (ms) | p95 |
| `current_p50` | float (ms) | p50 |
| `request_count_5m` | int | spans observed in the last 5 minutes |
| `error_rate_5m` | float (0–1) | fraction of spans with `status=error` |
| `breach_duration` | int (s) | seconds the p99 has continuously exceeded `target_max`; 0 when not breached |

States: `live` (healthy) → `degrading` (error rate ≥ 1%) → `breached` (p99 over target_max).

## Emitting spans to Tempo

**Any OpenTelemetry-instrumented service works** — Go, Java, .NET, Python, Node, Rust, PHP, Ruby, anything that speaks OTLP. The provider doesn't care about the language; it queries Tempo for the spans you sent.

### The contract

Tempo's metrics endpoint matches spans by **exact span name** plus optional attribute filters. Three things must be true:

1. **Span name in the model matches the emitted name verbatim.** `span: "checkout.init"` in YAML must equal what your tracer's `startSpan("checkout.init")` (or equivalent) produces.
2. **Status is set on errors.** `status=error` is what `error_rate_5m` counts. Most OTEL SDKs do this automatically when you call `setStatus(StatusCode.ERROR)` or when a span exits via an exception handler.
3. **Spans actually arrive.** Set `OTEL_EXPORTER_OTLP_ENDPOINT` to your collector (or directly to Tempo's OTLP receiver, port 4318), and `OTEL_SERVICE_NAME` so spans are attributable.

That's the whole contract. Everything below is a getting-started snippet for one popular language; the OTEL doc for *your* language has the canonical bootstrap.

### One-time service setup (any language)

Set these env vars on the container/process emitting spans:

```
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector.observability.svc:4318
OTEL_SERVICE_NAME=<your-service-name>
OTEL_RESOURCE_ATTRIBUTES=deployment.color=blue,service.version=2.4.7-p3
```

Then add the OTEL SDK + OTLP/HTTP exporter for your language. Per-language bootstrap docs:

| Language | OTEL bootstrap doc |
|---|---|
| Go | [opentelemetry.io/docs/languages/go](https://opentelemetry.io/docs/languages/go/) |
| Java | [opentelemetry.io/docs/languages/java](https://opentelemetry.io/docs/languages/java/) |
| Python | [opentelemetry.io/docs/languages/python](https://opentelemetry.io/docs/languages/python/) |
| Node.js / JS | [opentelemetry.io/docs/languages/js](https://opentelemetry.io/docs/languages/js/) |
| .NET | [opentelemetry.io/docs/languages/net](https://opentelemetry.io/docs/languages/net/) |
| PHP | [opentelemetry.io/docs/languages/php](https://opentelemetry.io/docs/languages/php/) |
| Rust | [opentelemetry.io/docs/languages/rust](https://opentelemetry.io/docs/languages/rust/) |
| Ruby | [opentelemetry.io/docs/languages/ruby](https://opentelemetry.io/docs/languages/ruby/) |

### Naming a span (one example — Go)

```go
ctx, span := tracer.Start(ctx, "checkout.init")
defer span.End()
// ... business logic
```

Equivalent in Java: `tracer.spanBuilder("checkout.init").startSpan()`. In Python: `tracer.start_as_current_span("checkout.init")`. In PHP: `$tracer->spanBuilder('checkout.init')->startSpan()`. The SDK shape varies; the **span name** is the one thing the provider's TraceQL query joins on.

### Verify spans arrive

Before wiring the model, hit Tempo directly with the same query the provider would send:

```bash
curl 'http://tempo:3200/api/metrics/query_range' \
  --data-urlencode 'q={ name = "checkout.init" } | quantile_over_time(.99, duration)' \
  --data-urlencode "start=$(date -d '5 minutes ago' +%s)" \
  --data-urlencode "end=$(date +%s)" \
  --data-urlencode 'step=30s'
```

Empty result → spans aren't reaching Tempo. Common causes: collector endpoint wrong, `OTEL_SERVICE_NAME` unset, span name typo.

## Example models

Two examples, deliberately separate so each tells one story end-to-end:

- [`examples/magento-platform.model.yaml`](./examples/magento-platform.model.yaml) — **steady-state SLOs.** Four customer-facing operations (catalog browse, add to cart, checkout init, search) each held to a three-number contract: latency budget, error budget, breach tolerance. Shows the everyday shape of `tracing.span_invariant`.
- [`examples/magento-blue-green-canary.model.yaml`](./examples/magento-blue-green-canary.model.yaml) — **the deployment moment.** A canary SLO that uses `span_filter` to scope to the just-promoted color, depending on a `kubernetes.service` so a stuck switch surfaces from two angles. Shows multi-provider composition and the `span_filter` var.

## Architecture

- `main.go` — 13 lines: registers types and calls `provider.Main`.
- `internal/probes/` — one ProbeFn per fact, builds TraceQL queries.
- `internal/tempoclient/` — HTTP client with timeout, auth headers, status-to-sentinel-error mapping.

Plumbing (argv parsing, exit codes, debug tracing) comes from [`mgtt/sdk/provider`](https://github.com/mgt-tool/mgtt/tree/main/sdk/provider).

## Development

```bash
go build .                    # compile
go test -race ./...           # unit tests
mgtt provider validate tempo  # static checks
```
