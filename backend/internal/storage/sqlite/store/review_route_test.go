package store_test

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestReviewPublicationRouteRoundTrip(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "route")
	rec, err := s.CreateSession(t.Context(), sampleRecord("route"))
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, s.UpsertReview(t.Context(), domain.Review{ID: "route-review", SessionID: rec.ID, ProjectID: rec.ProjectID, Harness: domain.ReviewerCodex, CreatedAt: now, UpdatedAt: now}))
	for _, route := range []domain.ReviewPublicationRoute{"", domain.ReviewPublicationMCP, domain.ReviewPublicationGlab} {
		t.Run(string(route.Normalized()), func(t *testing.T) {
			run := domain.ReviewRun{ID: "route-" + string(route.Normalized()), ReviewID: "route-review", SessionID: rec.ID, Harness: domain.ReviewerCodex,
				PRURL: "https://gitlab.example/g/p/-/merge_requests/1", TargetSHA: string(route.Normalized()), Status: domain.ReviewRunRunning, CreatedAt: now, PublicationRoute: route}
			require.NoError(t, s.InsertReviewRun(t.Context(), run))
			got, ok, err := s.GetReviewRun(t.Context(), run.ID)
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, route.Normalized(), got.PublicationRoute)
			_, err = s.UpdateReviewRunResult(t.Context(), run.ID, domain.ReviewRunComplete, domain.VerdictApproved, "done", "", false)
			require.NoError(t, err)
			got, _, err = s.GetReviewRun(t.Context(), run.ID)
			require.NoError(t, err)
			require.Equal(t, route.Normalized(), got.PublicationRoute)
		})
	}
}
