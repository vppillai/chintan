// Package provider is the boundary to the paid third-party models: the STT,
// LLM and Router interfaces the pipeline calls, and the Groq and OpenAI
// adapters behind them. What each prompt sends is docs/design/prompts.md; how
// long a call may take is docs/design/pipeline-deadlines.md. An adapter never
// logs a response body (scripts/check-log-hygiene.sh enforces it).
package provider
