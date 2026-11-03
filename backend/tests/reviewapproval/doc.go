//go:build e2e_test

// Package reviewapproval exercises reviewer approval ownership through the real
// review engine, SQLite store, Chat service, and Codex app-server.
//
// Local binding tests run without a model or provider request. The native test
// is skipped unless -reviewapproval.live is explicitly supplied with a new
// -reviewapproval.lab-root under the user's .ao/labs directory and a lab-owned
// -reviewapproval.ao-binary. It requires a separately allocated live scenario:
// two harmless review runs, one service/store restart, and human resolution of
// the second run's exact pending command. It never publishes a provider review.
//
// This is native/service integration, not public CLI trigger or daemon startup
// coverage. The test-only bridge replaces the first user message, while keeping
// production system instructions, approval validation, sandbox, native history,
// cancellation, persistence, and the existing HTTP approval resolver.
package reviewapproval
