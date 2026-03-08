// Package observability provides OpenTelemetry tracing wrappers for LLM
// clients, container managers, and scenario validation.
//
// InitTracer sets up a TracerProvider; it returns a noop provider when no OTLP
// endpoint is configured, so callers do not need to special-case the disabled
// case. TracingLLMClient wraps an llm.Client and records spans with attributes
// for input tokens, output tokens, and estimated cost on each Generate and
// Judge call.
package observability
