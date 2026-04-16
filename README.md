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

## Install

```bash
mgtt provider install tempo
```

## Auth

| Variable | Purpose | Required |
|---|---|---|
| `tempo_url` | Base URL of the Tempo HTTP API | yes |
| `auth_token` | Bearer token (when Tempo is fronted by auth) | no |
| `tenant_id` | `X-Scope-OrgID` for multi-tenant Tempo | no |

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

Tempo only knows about spans you send it. The probe queries match by **span name verbatim** — your `span: "checkout.init"` in the model has to match what your code emits.

### From PHP / Magento

Drop the OpenTelemetry PHP SDK into the php-fpm container. Bootstrap (`pub/index.php` or a Magento module init):

```php
use OpenTelemetry\SDK\Trace\TracerProvider;
use OpenTelemetry\SDK\Trace\SpanProcessor\BatchSpanProcessor;
use OpenTelemetry\Contrib\Otlp\SpanExporter;
use OpenTelemetry\SDK\Common\Export\Http\PsrTransportFactory;

$transport = (new PsrTransportFactory())
    ->create(getenv('OTEL_EXPORTER_OTLP_ENDPOINT') . '/v1/traces', 'application/x-protobuf');

$provider = TracerProvider::builder()
    ->addSpanProcessor(new BatchSpanProcessor(new SpanExporter($transport)))
    ->build();

$tracer = $provider->getTracer('magento');
```

Container env vars:

```
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector.observability.svc:4318
OTEL_SERVICE_NAME=magento-php-fpm
OTEL_RESOURCE_ATTRIBUTES=deployment.color=blue,service.version=2.4.7-p3
```

In your business code:

```php
$span = $tracer->spanBuilder('checkout.init')->startSpan();
try {
    // ... checkout init logic
} finally {
    $span->end();
}
```

### From Go / Node / Python

Use the official OTEL SDK for the language plus the OTLP/HTTP exporter pointed at your collector. The exact same span name + attributes pattern applies.

### Verify spans arrive

Before wiring the model, hit Tempo directly:

```bash
curl 'http://tempo:3200/api/metrics/query_range' \
  --data-urlencode 'q={ name = "checkout.init" } | quantile_over_time(.99, duration)' \
  --data-urlencode "start=$(date -d '5 minutes ago' +%s)" \
  --data-urlencode "end=$(date +%s)" \
  --data-urlencode 'step=30s'
```

If the result is empty, the spans aren't reaching Tempo. Common causes: collector endpoint wrong, `OTEL_SERVICE_NAME` unset, span name has unexpected characters.

## Example model

See [`examples/magento-platform.model.yaml`](./examples/magento-platform.model.yaml) — wires four `tracing.span_invariant` components against a real Magento checkout flow alongside `kubernetes.*` infra.

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
