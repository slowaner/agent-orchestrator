package review

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestSubmitGitLabRejectsGitHubReviewID(t *testing.T) {
	store := &fakeStore{ok: true, run: domain.ReviewRun{ID: "run-1", SessionID: "worker-1", PRURL: "https://gitlab.example/group/repo/-/merge_requests/39", Status: domain.ReviewRunRunning}}
	service := New(nil, store)

	_, err := service.Submit(t.Context(), "worker-1", "run-1", domain.VerdictApproved, "No findings", "invented-id")

	require.ErrorContains(t, err, "GitLab publication must not use a GitHub review id")
	assert.Zero(t, store.updateCalls)
}

func TestSubmitGitLabPublicationFailureKeepsLocalVerdict(t *testing.T) {
	store := &fakeStore{ok: true, run: domain.ReviewRun{ID: "run-1", SessionID: "worker-1", PRURL: "https://gitlab.example/group/repo/-/merge_requests/39", Status: domain.ReviewRunRunning}}
	service := New(nil, store)
	body := "No findings.\n\nProvider publication failed or is unconfirmed: permission denied."

	run, err := service.Submit(t.Context(), "worker-1", "run-1", domain.VerdictApproved, body, "")

	require.NoError(t, err)
	assert.Equal(t, domain.VerdictApproved, run.Verdict)
	assert.Equal(t, body, run.Body)
	assert.Empty(t, run.GithubReviewID)
}

func TestSubmitManyMixedProvidersKeepGitHubIDsSeparate(t *testing.T) {
	store := &fakeStore{batchRuns: []domain.ReviewRun{
		{ID: "gh-run", SessionID: "worker-1", BatchID: "batch-1", PRURL: "https://github.com/o/r/pull/1", Status: domain.ReviewRunRunning},
		{ID: "gl-run", SessionID: "worker-1", BatchID: "batch-1", PRURL: "https://gitlab.example/g/sub/r/-/merge_requests/1", Status: domain.ReviewRunRunning},
	}}
	service := New(nil, store)

	runs, err := service.SubmitMany(t.Context(), "worker-1", []SubmittedReview{
		{RunID: "gh-run", Verdict: domain.VerdictApproved, Body: "GitHub publication confirmed", GithubReviewID: "12345"},
		{RunID: "gl-run", Verdict: domain.VerdictApproved, Body: "Provider publication unconfirmed: timeout; draft 42. Worker delivery unconfirmed."},
	})

	require.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, "12345", runs[0].GithubReviewID)
	assert.Empty(t, runs[1].GithubReviewID)
	assert.Contains(t, runs[1].Body, "unconfirmed")
	assert.Equal(t, domain.VerdictApproved, runs[1].Verdict)
}

func TestSubmitGitLabLegacyResultsRemainReplayable(t *testing.T) {
	for _, status := range []domain.ReviewRunStatus{domain.ReviewRunComplete, domain.ReviewRunDelivered} {
		t.Run(string(status), func(t *testing.T) {
			original := domain.ReviewRun{ID: "old-run", SessionID: "worker-1", PRURL: "https://gitlab.example/g/r/-/merge_requests/1", Status: status, Verdict: domain.VerdictApproved, Body: "Historical result", GithubReviewID: "legacy-id"}
			store := &fakeStore{ok: true, run: original}
			service := New(nil, store)

			run, err := service.Submit(t.Context(), "worker-1", original.ID, original.Verdict, original.Body, original.GithubReviewID)

			require.NoError(t, err)
			assert.Equal(t, original, run)
			assert.Zero(t, store.updateCalls)
		})
	}
}

func TestSubmitManySupersededGitLabKeepsGitHubSibling(t *testing.T) {
	store := &fakeStore{batchRuns: []domain.ReviewRun{
		{ID: "old-gl", SessionID: "worker-1", PRURL: "https://gitlab.example/g/r/-/merge_requests/1", Status: domain.ReviewRunCancelled, GithubReviewID: "legacy-id"},
		{ID: "gh-run", SessionID: "worker-1", PRURL: "https://github.com/o/r/pull/2", Status: domain.ReviewRunRunning},
	}}
	service := New(nil, store)

	runs, err := service.SubmitMany(t.Context(), "worker-1", []SubmittedReview{
		{RunID: "old-gl", Verdict: domain.VerdictApproved, GithubReviewID: "legacy-id"},
		{RunID: "gh-run", Verdict: domain.VerdictApproved, GithubReviewID: "12345"},
	})

	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, "gh-run", runs[0].ID)
	assert.Equal(t, "12345", runs[0].GithubReviewID)
	assert.Equal(t, domain.ReviewRunCancelled, store.batchRuns[0].Status)
	assert.Equal(t, "legacy-id", store.batchRuns[0].GithubReviewID)
	assert.Equal(t, 1, store.updateCalls)
}
