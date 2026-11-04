package chat_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/stretchr/testify/require"
)

func TestReviewerApprovalScopeIsCheckedAgainstDurableRunBeforeDecision(t *testing.T) {
	st := openStore(t)
	now := time.Now().UTC()
	require.NoError(t, st.UpsertReview(t.Context(), domain.Review{ID: "review-approval", SessionID: testSession, ProjectID: testProject, Harness: domain.ReviewerCodex, CreatedAt: now, UpdatedAt: now}))
	run := domain.ReviewRun{ID: "run-approval", ReviewID: "review-approval", SessionID: testSession, BatchID: "batch-approval", Harness: domain.ReviewerCodex, PRURL: "https://gitlab.example.test/group/repo/-/merge_requests/1", TargetSHA: "0123456789012345678901234567890123456789", Status: domain.ReviewRunRunning, CreatedAt: now}
	require.NoError(t, st.InsertReviewRun(t.Context(), run))
	provider := newFakeConversation()
	var started ports.ChatStartConfig
	svc := chatsvc.New(chatsvc.Options{Store: st, Sessions: st, Drivers: fakeRegistry{driver: fakeDriver{conv: provider, startCfg: &started}}, Log: slog.New(slog.DiscardHandler), NewID: func() string { return "approval-conversation" }})
	owner := domain.ReviewConversationOwner(run.ReviewID)
	t.Cleanup(func() { _ = svc.StopForOwner(context.WithoutCancel(t.Context()), owner) })
	scope := &ports.ReviewerApprovalScope{ReviewID: run.ReviewID, RunID: run.ID, WorkerID: testSession, BatchID: run.BatchID, PRURL: run.PRURL, TargetSHA: run.TargetSHA}
	_, err := svc.Start(t.Context(), chatsvc.StartConfig{Owner: owner, SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(), ReviewerApproval: scope})
	require.NoError(t, err)
	require.True(t, started.ReadOnly)
	require.Equal(t, scope, started.ReviewerApproval)
	require.NoError(t, svc.ResolveForOwner(t.Context(), owner, "pending-1", ports.ChatDecision{ID: "accept"}))
	count, err := st.CancelRunningReviewRunsBySession(t.Context(), testSession, "cancel fixture")
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.ErrorIs(t, svc.ResolveForOwner(t.Context(), owner, "pending-2", ports.ChatDecision{ID: "accept"}), ports.ErrChatRequestNotPending)
	provider.mu.Lock()
	defer provider.mu.Unlock()
	_, resolved := provider.resolved["pending-2"]
	require.False(t, resolved, "stale run must be rejected before native resolution")
}

func TestReviewerApprovalScopeRejectsWrongOwnerTargetAndHead(t *testing.T) {
	for _, change := range []string{"owner", "worker", "run", "batch", "target", "head", "completed"} {
		t.Run(change, func(t *testing.T) {
			st := openStore(t)
			now := time.Now().UTC()
			require.NoError(t, st.UpsertReview(t.Context(), domain.Review{ID: "review-approval", SessionID: testSession, ProjectID: testProject, Harness: domain.ReviewerCodex, CreatedAt: now, UpdatedAt: now}))
			run := domain.ReviewRun{ID: "run-approval", ReviewID: "review-approval", SessionID: testSession, BatchID: "batch-approval", Harness: domain.ReviewerCodex, PRURL: "https://gitlab.example.test/group/repo/-/merge_requests/1", TargetSHA: "0123456789012345678901234567890123456789", Status: domain.ReviewRunRunning, CreatedAt: now}
			if change == "completed" {
				run.Status = domain.ReviewRunComplete
			}
			require.NoError(t, st.InsertReviewRun(t.Context(), run))
			scope := &ports.ReviewerApprovalScope{ReviewID: run.ReviewID, RunID: run.ID, WorkerID: testSession, BatchID: run.BatchID, PRURL: run.PRURL, TargetSHA: run.TargetSHA}
			owner := domain.ReviewConversationOwner(run.ReviewID)
			switch change {
			case "owner":
				owner = domain.SessionConversationOwner(testSession)
			case "worker":
				scope.WorkerID = "other-worker"
			case "run":
				scope.RunID = "other-run"
			case "batch":
				scope.BatchID = "other-batch"
			case "target":
				scope.PRURL = "https://gitlab.example.test/group/other/-/merge_requests/1"
			case "head":
				scope.TargetSHA = "different-head"
			}
			svc := chatsvc.New(chatsvc.Options{Store: st, Sessions: st, Drivers: fakeRegistry{driver: fakeDriver{conv: newFakeConversation()}}, Log: slog.New(slog.DiscardHandler), NewID: func() string { return "approval-conversation" }})
			_, err := svc.Start(t.Context(), chatsvc.StartConfig{Owner: owner, SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(), ReviewerApproval: scope})
			require.ErrorIs(t, err, ports.ErrChatRequestNotPending)
		})
	}
}

func TestReviewerRestoredNilScopeCannotResolveCancelledApproval(t *testing.T) {
	st := openStore(t)
	now := time.Now().UTC()
	require.NoError(t, st.UpsertReview(t.Context(), domain.Review{ID: "cancelled-review", SessionID: testSession, ProjectID: testProject, Harness: domain.ReviewerCodex, CreatedAt: now, UpdatedAt: now}))
	require.NoError(t, st.InsertReviewRun(t.Context(), domain.ReviewRun{ID: "cancelled-run", ReviewID: "cancelled-review", SessionID: testSession, Harness: domain.ReviewerCodex, PRURL: "https://gitlab.example.test/group/repo/-/merge_requests/1", TargetSHA: "0123456789012345678901234567890123456789", Status: domain.ReviewRunCancelled, CreatedAt: now}))
	provider := newFakeConversation()
	svc := chatsvc.New(chatsvc.Options{Store: st, Sessions: st, Drivers: fakeRegistry{driver: fakeDriver{conv: provider}}, Log: slog.New(slog.DiscardHandler), NewID: func() string { return "cancelled-conversation" }})
	owner := domain.ReviewConversationOwner("cancelled-review")
	t.Cleanup(func() { _ = svc.StopForOwner(context.WithoutCancel(t.Context()), owner) })
	_, err := svc.Start(t.Context(), chatsvc.StartConfig{Owner: owner, SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, WorkspacePath: t.TempDir()})
	require.NoError(t, err)
	require.ErrorIs(t, svc.ResolveForOwner(t.Context(), owner, "0", ports.ChatDecision{ID: "accept"}), ports.ErrChatRequestNotPending)
	provider.mu.Lock()
	defer provider.mu.Unlock()
	require.Empty(t, provider.resolved, "stale decision must not reach native provider")
}
