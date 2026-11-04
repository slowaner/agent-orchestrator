package lifecycle

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/reviewtrace"
	"github.com/stretchr/testify/require"
)

func TestReviewTraceDistinguishesDeliverySignatureAndDedup(t *testing.T) {
	m, store, messenger := newManager()
	store.sessions["mer-1"] = working("mer-1")
	store.comments["pr1"] = []domain.PullRequestComment{{ID: "note-1", ThreadID: "thread-1", Author: "reviewer", File: "fixture.go", Line: 8, Body: "finding", AutoInjectReview: true}}
	var output bytes.Buffer
	ctx := reviewtrace.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, nil)))
	ctx = reviewtrace.WithRequests(ctx, []ports.ReviewRefreshRequest{{RunID: "run-1", WorkerID: "mer-1", PRURL: "pr1"}})
	observation := ports.PRObservation{Fetched: true, URL: "pr1", Review: domain.ReviewChangesRequest}
	require.NoError(t, m.ApplyPRObservation(ctx, "mer-1", observation))
	require.Len(t, messenger.msgs, 1)
	require.Contains(t, output.String(), `"event":"worker_delivery","outcome":"sent"`)
	require.Contains(t, output.String(), `"event":"delivery_signature","outcome":"persisted"`)
	require.Contains(t, output.String(), `"note_id":"note-1"`)
	require.NotContains(t, output.String(), "finding")
	output.Reset()
	require.NoError(t, m.ApplyPRObservation(ctx, "mer-1", observation))
	require.Len(t, messenger.msgs, 1)
	require.Contains(t, output.String(), `"outcome":"deduplicated"`)
	require.NotContains(t, output.String(), `"outcome":"sent"`)
}
