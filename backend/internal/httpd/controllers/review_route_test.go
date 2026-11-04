package controllers_test

import (
	"net/http"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestReviewTriggerForwardsPublicationRoute(t *testing.T) {
	service := &fakeReviewService{}
	server := newReviewTestServer(t, service)
	_, status, _ := doRequest(t, server, "POST", "/api/v1/sessions/mer-1/reviews/trigger", `{"prUrl":"https://gitlab.example/g/p/-/merge_requests/1","publicationRoute":"glab"}`)
	require.Equal(t, http.StatusCreated, status)
	require.Equal(t, domain.ReviewPublicationGlab, service.triggerRequest.PublicationRoute)
}
