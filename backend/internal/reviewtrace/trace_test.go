package reviewtrace

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestTraceScopesCoalescedRequestsAndUsesDefaultInfoLevel(t *testing.T) {
	var output bytes.Buffer
	ctx := WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, nil)))
	requests := []ports.ReviewRefreshRequest{
		{RunID: "run-a", WorkerID: "worker-a", PRURL: "https://private.invalid/g/p/-/merge_requests/1", TargetSHA: "head-a"},
		{RunID: "run-b", WorkerID: "worker-a", PRURL: "https://private.invalid/g/p/-/merge_requests/1", TargetSHA: "head-a"},
		{RunID: "other-worker", WorkerID: "worker-b", PRURL: "https://private.invalid/g/p/-/merge_requests/1"},
		{RunID: "other-mr", WorkerID: "worker-a", PRURL: "https://private.invalid/g/p/-/merge_requests/2"},
	}
	ctx = WithPoll(WithRequests(ctx, requests), "poll-1", "timer")
	requests[0].RunID = "mutated"
	ctx = ForTarget(ctx, "worker-a", "https://private.invalid/g/p/-/merge_requests/1")
	Emit(WithComment(ctx, "note-1", "discussion-1"), "provider_fetch", "ok", slog.String("fetch_reason", "forced"), slog.String("body", "private body"), slog.String("error", "private error"))
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, lines, 2)
	for i, line := range lines {
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		require.Equal(t, []string{"run-a", "run-b"}[i], event["run_id"])
		require.Equal(t, "worker-a", event["worker_id"])
		require.Equal(t, "timer", event["dispatch_cause"])
		require.Equal(t, "forced", event["fetch_reason"])
		require.Equal(t, "note-1", event["note_id"])
		require.NotContains(t, line, "private")
	}
	output.Reset()
	Emit(ForTarget(ctx, "worker-a", "unrelated"), "provider_fetch", "ok")
	require.NotContains(t, output.String(), "run_id")
	require.NotContains(t, output.String(), "worker_id")
	output.Reset()
	Emit(WithLogger(ctx, nil), "provider_fetch", "ok")
	require.Empty(t, output.String())
}
