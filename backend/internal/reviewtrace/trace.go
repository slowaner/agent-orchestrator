package reviewtrace

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type key struct{}
type state struct {
	logger                          *slog.Logger
	pollID, cause, noteID, threadID string
	requests                        []ports.ReviewRefreshRequest
}

func current(ctx context.Context) state { s, _ := ctx.Value(key{}).(state); return s }

// WithLogger enables local tracing for this context only. A nil logger disables
// tracing; neither product telemetry nor cloud exporters are called.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	s := current(ctx)
	s.logger = logger
	return context.WithValue(ctx, key{}, s)
}

// WithPoll attaches the actual dispatcher identity and cause to downstream work.
func WithPoll(ctx context.Context, id, cause string) context.Context {
	s := current(ctx)
	s.pollID, s.cause = id, cause
	return context.WithValue(ctx, key{}, s)
}

// WithRequests copies all coalesced request tokens into this observation context.
func WithRequests(ctx context.Context, requests []ports.ReviewRefreshRequest) context.Context {
	s := current(ctx)
	s.requests = slices.Clone(requests)
	return context.WithValue(ctx, key{}, s)
}

// ForTarget restricts events to one target and its owning worker.
func ForTarget(ctx context.Context, workerID string, urls ...string) context.Context {
	s := current(ctx)
	var matches []ports.ReviewRefreshRequest
	for _, r := range s.requests {
		if string(r.WorkerID) == workerID && slices.Contains(urls, r.PRURL) {
			matches = append(matches, r)
		}
	}
	s.requests = matches
	return context.WithValue(ctx, key{}, s)
}

// WithComment binds a lifecycle event to provider object IDs, never its body.
func WithComment(ctx context.Context, noteID, threadID string) context.Context {
	s := current(ctx)
	s.noteID, s.threadID = noteID, threadID
	return context.WithValue(ctx, key{}, s)
}

// Emit writes one local event per retained token, or one uncorrelated periodic
// event at Info level. Only bounded stage/reason/head and numeric fields are accepted.
// Target URLs, arbitrary errors, commands, bodies and auth fields cannot be attached.
func Emit(ctx context.Context, event, outcome string, attrs ...slog.Attr) {
	s := current(ctx)
	if s.logger == nil {
		return
	}
	base := []slog.Attr{slog.String("event", safeID(event)), slog.String("outcome", safeID(outcome)), slog.String("poll_id", safeID(s.pollID)), slog.String("dispatch_cause", safeID(s.cause))}
	if s.noteID != "" {
		base = append(base, slog.String("note_id", safeID(s.noteID)))
	}
	if s.threadID != "" {
		base = append(base, slog.String("thread_id", safeID(s.threadID)))
	}
	for _, a := range attrs {
		switch a.Key {
		case "stage", "observed_head_sha", "fetch_reason":
			if a.Value.Kind() == slog.KindString {
				base = append(base, slog.String(a.Key, safeID(a.Value.String())))
			}
		case "duration_ms", "count":
			if a.Value.Kind() == slog.KindInt64 {
				base = append(base, a)
			}
		}
	}
	if len(s.requests) == 0 {
		s.logger.LogAttrs(ctx, slog.LevelInfo, "review trace", base...)
		return
	}
	for _, r := range s.requests {
		fields := append(slices.Clone(base), slog.String("run_id", safeID(r.RunID)), slog.String("worker_id", safeID(string(r.WorkerID))), slog.String("target_key", digest(r.PRURL)), slog.String("reviewed_head_sha", safeID(r.TargetSHA)))
		s.logger.LogAttrs(ctx, slog.LevelInfo, "review trace", fields...)
	}
}

func digest(raw string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(raw))) }
func safeID(raw string) string {
	if len(raw) > 128 || strings.IndexFunc(raw, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._-", r))
	}) >= 0 {
		return digest(raw)
	}
	return raw
}
