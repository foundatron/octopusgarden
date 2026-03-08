// Package llm defines a model-agnostic client interface for LLM interactions
// with Anthropic and OpenAI backend implementations.
//
// The Client interface exposes two methods: Generate for code generation and
// Judge for satisfaction scoring. Both methods track token counts and estimated
// cost per call. Generate supports prompt caching via CacheControl to reduce
// cost on repeated spec content across attractor iterations. The Anthropic and
// OpenAI implementations are interchangeable; provider selection is handled by
// the calling layer.
package llm
