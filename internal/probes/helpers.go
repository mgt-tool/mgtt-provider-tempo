// Package probes implements the tempo-provider probe surface. All plumbing
// (argv parsing, exit codes, status:not_found translation) lives in the SDK;
// this package only constructs TraceQL queries and parses Tempo responses.
package probes

import (
	"fmt"

	"github.com/mgt-tool/mgtt-provider-tempo/internal/tempoclient"
	"github.com/mgt-tool/mgtt/sdk/provider"
)

// NewTempoConstructor is overridable for tests.
var NewTempoConstructor = func(baseURL, token, tenantID string) *tempoclient.Client {
	return tempoclient.New(baseURL, token, tenantID)
}

// resolveSpan extracts the required `span` flag (the OTEL span name to query).
func resolveSpan(req provider.Request) (string, error) {
	if s := req.Extra["span"]; s != "" {
		return s, nil
	}
	return "", fmt.Errorf("%w: tracing.span_invariant requires --span <name>", provider.ErrUsage)
}

// resolveTargetMax extracts the required `target_max` flag (a duration like
// "800ms", "2s"). Returned as the raw string for downstream parsing.
func resolveTargetMax(req provider.Request) (string, error) {
	if t := req.Extra["target_max"]; t != "" {
		return t, nil
	}
	return "", fmt.Errorf("%w: tracing.span_invariant requires --target_max <duration>", provider.ErrUsage)
}

// newClient constructs a Tempo HTTP client from request extras.
func newClient(req provider.Request) (*tempoclient.Client, error) {
	url := req.Extra["tempo_url"]
	if url == "" {
		return nil, fmt.Errorf("%w: tempo provider requires --tempo_url <url>", provider.ErrUsage)
	}
	return NewTempoConstructor(url, req.Extra["auth_token"], req.Extra["tenant_id"]), nil
}

// Register adds the tempo provider's types to the registry.
func Register(r *provider.Registry) {
	registerSpanInvariant(r)
}
