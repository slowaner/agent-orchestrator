package daemon

import (
	"bytes"
	"log/slog"
	"testing"

	scmobserve "github.com/aoagents/agent-orchestrator/backend/internal/observe/scm"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/reviewtrace"
	"github.com/stretchr/testify/require"
)

func TestReviewRefreshBridgeUsesOptInLocalLogger(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	request := ports.ReviewRefreshRequest{RunID: "run", WorkerID: "worker", PRURL: "private-target", TargetSHA: "head"}
	ctx := reviewtrace.WithRequests(reviewtrace.WithLogger(t.Context(), logger), []ports.ReviewRefreshRequest{request})
	bridge := &scmReviewRefresher{}
	bridge.RequestReviewRefresh(ctx, request)
	require.Contains(t, output.String(), "event=refresh_dropped outcome=unavailable")
	output.Reset()
	// No provider, daemon, session or poll is started: only the actual bridge and queue run.
	observer := scmobserve.New(nil, nil, nil, scmobserve.Config{Logger: logger, TraceEvents: true})
	bridge.Bind(observer)
	bridge.RequestReviewRefresh(ctx, request)
	require.Contains(t, output.String(), "event=refresh_bridge outcome=bound")
	require.Contains(t, output.String(), "event=refresh_enqueued outcome=queued")
	require.NotContains(t, output.String(), request.PRURL)
	require.True(t, newLogger().Enabled(t.Context(), slog.LevelInfo))
	output.Reset()
	observer = scmobserve.New(nil, nil, nil, scmobserve.Config{Logger: logger})
	observer.RequestReviewRefresh(t.Context(), request)
	require.Empty(t, output.String())
}
