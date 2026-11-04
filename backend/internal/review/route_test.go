package review

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestTriggerPersistsRouteAndRefusesActiveTransportSwitch(t *testing.T) {
	const target = "https://gitlab.example/g/sub/p/-/merge_requests/1"
	store := &fakeStore{}
	launcher := &fakeLauncher{handle: "review-mer-1", alive: true}
	prs := prAt("sha1")
	prs.prs[0].URL, prs.prs[0].HTMLURL = target, target
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prs, fakeProjects{}, launcher)
	result, err := eng.TriggerWithOptions(t.Context(), "mer-1", TriggerOptions{PRURL: target, PublicationRoute: domain.ReviewPublicationGlab})
	require.NoError(t, err)
	require.True(t, result.Created)
	require.Equal(t, domain.ReviewPublicationGlab, result.Run.PublicationRoute)
	require.Equal(t, domain.ReviewPublicationGlab, launcher.gotSpec.PublicationRoute)
	require.Equal(t, domain.ReviewPublicationGlab, launcher.gotSpec.ReviewQueue[0].PublicationRoute)
	_, err = eng.TriggerWithOptions(t.Context(), "mer-1", TriggerOptions{PRURL: target, PublicationRoute: domain.ReviewPublicationMCP})
	require.ErrorIs(t, err, ErrInvalid)
	require.Equal(t, domain.ReviewPublicationGlab, store.runs[0].PublicationRoute)
	queue := reviewQueue(store.runs)
	require.Equal(t, domain.ReviewPublicationGlab, queue[0].PublicationRoute)
}

func TestRestoreReviewerRetainsPerRunPublicationRoutes(t *testing.T) {
	store := &fakeStore{
		review: &domain.Review{ID: "review", SessionID: "mer-1", Harness: domain.ReviewerCodex, ReviewerHandleID: "review-mer-1", AgentSessionID: "native"},
		runs: []domain.ReviewRun{
			{ID: "glab", ReviewID: "review", SessionID: "mer-1", Harness: domain.ReviewerCodex, PRURL: "https://gitlab.example/g/a/-/merge_requests/1", Status: domain.ReviewRunRunning, PublicationRoute: domain.ReviewPublicationGlab},
			{ID: "mcp", ReviewID: "review", SessionID: "mer-1", Harness: domain.ReviewerCodex, PRURL: "https://gitlab.example/g/b/-/merge_requests/2", Status: domain.ReviewRunComplete, PublicationRoute: domain.ReviewPublicationMCP},
		},
	}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerCodex
	launcher := &fakeLauncher{handle: "review-mer-1"}
	engine := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	result, err := engine.RestoreReviewer(t.Context(), "mer-1")
	require.NoError(t, err)
	require.True(t, result.Restored)
	require.Len(t, launcher.gotSpec.PreviousRuns, 2)
	require.Equal(t, domain.ReviewPublicationGlab, launcher.gotSpec.PreviousRuns[0].PublicationRoute)
	require.Equal(t, domain.ReviewPublicationMCP, launcher.gotSpec.PreviousRuns[1].PublicationRoute)
	history := previousReviewHistoryText(launcher.gotSpec.PreviousRuns)
	require.Contains(t, history, "selected publication route: glab (instructions only)")
	require.Contains(t, history, "selected publication route: mcp (instructions only)")
}
