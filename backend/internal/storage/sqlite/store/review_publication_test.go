package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// These URLs are persistence/binding fixtures only; no provider calls occur.
func TestReviewPublicationRoundTripAndFeedbackPolicy(t *testing.T) {
	const target = "https://gitlab.example/group/sub/repo/-/merge_requests/39"
	for _, tc := range []struct {
		commentID      string
		name           string
		runURL         string
		commentURL     string
		githubID       string
		publication    *domain.ReviewPublication
		snapshot       bool
		observedPolicy bool
		wantPolicy     bool
		wantExternal   bool
	}{
		{commentID: "note-1", name: "GitLab snapshot enabled", runURL: target, commentURL: target, publication: &domain.ReviewPublication{Provider: "gitlab", Status: "published", PRURL: target, TargetSHA: "head", Refs: []domain.ReviewPublicationRef{{Kind: "discussion", ID: "discussion-1"}, {Kind: "note", ID: "note-1"}, {Kind: "note", ID: "summary-1"}}}, snapshot: true, wantPolicy: true},
		{commentID: "note-1", name: "GitLab snapshot disabled", runURL: target, commentURL: target, publication: &domain.ReviewPublication{Provider: "gitlab", Status: "published", PRURL: target, TargetSHA: "head", Refs: []domain.ReviewPublicationRef{{Kind: "discussion", ID: "discussion-1"}, {Kind: "note", ID: "note-1"}, {Kind: "note", ID: "summary-1"}}}, observedPolicy: true},
		{commentID: "note-1", name: "same IDs different MR", runURL: target, commentURL: "https://gitlab.example/group/sub/repo/-/merge_requests/40", publication: &domain.ReviewPublication{Provider: "gitlab", Status: "published", PRURL: target, TargetSHA: "head", Refs: []domain.ReviewPublicationRef{{Kind: "discussion", ID: "discussion-1"}, {Kind: "note", ID: "note-1"}}}, snapshot: true, wantExternal: true},
		{commentID: "note-1", name: "same IDs different project", runURL: target, commentURL: "https://gitlab.example/other/sub/repo/-/merge_requests/39", publication: &domain.ReviewPublication{Provider: "gitlab", Status: "published", PRURL: target, TargetSHA: "head", Refs: []domain.ReviewPublicationRef{{Kind: "discussion", ID: "discussion-1"}, {Kind: "note", ID: "note-1"}}}, snapshot: true, wantExternal: true},
		{commentID: "note-1", name: "blocked receipt", runURL: target, commentURL: target, publication: &domain.ReviewPublication{Provider: "gitlab", Status: "blocked", PRURL: target, TargetSHA: "head", Error: "denied", Refs: []domain.ReviewPublicationRef{{Kind: "discussion", ID: "discussion-1"}, {Kind: "note", ID: "note-1"}}}, snapshot: true, wantExternal: true},
		{commentID: "note-1", name: "unconfirmed receipt", runURL: target, commentURL: target, publication: &domain.ReviewPublication{Provider: "gitlab", Status: "unconfirmed", PRURL: target, TargetSHA: "head", Error: "timeout", Refs: []domain.ReviewPublicationRef{{Kind: "discussion", ID: "discussion-1"}, {Kind: "note", ID: "note-1"}}}, snapshot: true, wantExternal: true},
		{commentID: "reply-2", name: "other reply in native discussion", runURL: target, commentURL: target, publication: &domain.ReviewPublication{Provider: "gitlab", Status: "published", PRURL: target, TargetSHA: "head", Refs: []domain.ReviewPublicationRef{{Kind: "discussion", ID: "discussion-1"}, {Kind: "note", ID: "note-1"}}}, observedPolicy: true, wantPolicy: true, wantExternal: true},
		{commentID: "note-1", name: "legacy GitHub enabled", runURL: "https://github.com/o/r/pull/1", commentURL: "https://github.com/o/r/pull/1", githubID: "gh-review", snapshot: true, observedPolicy: true, wantPolicy: true},
		{commentID: "note-1", name: "legacy GitHub disabled", runURL: "https://github.com/o/r/pull/1", commentURL: "https://github.com/o/r/pull/1", githubID: "gh-review"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			seedProject(t, s, "mer")
			record := sampleRecord("mer")
			record.Activity.State = domain.ActivityActive
			session, err := s.CreateSession(t.Context(), record)
			require.NoError(t, err)
			now := time.Now().UTC().Truncate(time.Second)
			require.NoError(t, s.UpsertReview(t.Context(), domain.Review{ID: "review", SessionID: session.ID, ProjectID: "mer", Harness: "claude-code", PRURL: tc.runURL, CreatedAt: now, UpdatedAt: now}))
			require.NoError(t, s.InsertReviewRun(t.Context(), domain.ReviewRun{ID: "run", ReviewID: "review", SessionID: session.ID, BatchID: "batch", Harness: "claude-code", PRURL: tc.runURL, TargetSHA: "head", Status: domain.ReviewRunRunning, CreatedAt: now}))

			updated, err := s.UpdateReviewRunResult(t.Context(), "run", domain.ReviewRunComplete, domain.VerdictChangesRequested, "Fix main.go", tc.githubID, tc.snapshot, tc.publication)
			require.NoError(t, err)
			require.True(t, updated)
			run, found, err := s.GetReviewRun(t.Context(), "run")
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, tc.publication, run.Publication)
			assert.Equal(t, tc.githubID, run.GithubReviewID)
			assert.Equal(t, tc.snapshot, run.AutoInjectReview)
			batch, err := s.ListReviewRunsByBatch(t.Context(), session.ID, "batch")
			require.NoError(t, err)
			assert.Equal(t, []domain.ReviewRun{run}, batch)

			comments := []domain.PullRequestComment{{ID: tc.commentID, ThreadID: "discussion-1", ReviewID: tc.githubID, Author: "reviewer", File: "main.go", Line: 7, Body: "Fix main.go", URL: tc.commentURL + "#note_1", CreatedAt: now, AutoInjectReview: tc.observedPolicy}}
			pr := domain.PullRequest{URL: tc.commentURL, SessionID: session.ID, Number: 39, HeadSHA: "head", Review: domain.ReviewRequired, UpdatedAt: now, ObservedAt: now}
			threads := []domain.PullRequestReviewThread{{ThreadID: "discussion-1", Path: "main.go", Line: 7, UpdatedAt: now}}
			require.NoError(t, s.WriteSCMObservation(t.Context(), pr, nil, nil, threads, comments, ports.ReviewWriteReplace))

			feedback, err := s.ListPRComments(t.Context(), tc.commentURL)
			require.NoError(t, err)
			require.Len(t, feedback, 1)
			assert.Equal(t, tc.wantPolicy, feedback[0].AutoInjectReview)
			assert.Equal(t, "discussion-1", feedback[0].ThreadID)
			assert.Equal(t, "main.go", feedback[0].File)
			facts, err := s.ListPRFactsForSession(t.Context(), session.ID)
			require.NoError(t, err)
			require.Len(t, facts, 1)
			assert.True(t, facts[0].ReviewComments)
			assert.Equal(t, tc.wantExternal, facts[0].ExternalComments)
			many, err := s.ListPRFactsForSessions(t.Context(), []domain.SessionID{session.ID})
			require.NoError(t, err)
			assert.Equal(t, facts, many[session.ID])

			// A subsequent replacement poll cannot change the receipt policy or
			// the original comment's policy snapshot for legacy/external input.
			comments[0].AutoInjectReview = !tc.observedPolicy
			require.NoError(t, s.WriteSCMObservation(t.Context(), pr, nil, nil, threads, comments, ports.ReviewWriteReplace))
			feedback, err = s.ListPRComments(t.Context(), tc.commentURL)
			require.NoError(t, err)
			require.Len(t, feedback, 1)
			assert.Equal(t, tc.wantPolicy, feedback[0].AutoInjectReview)

			messenger := &publicationMessenger{}
			messenger.Test(t)
			messenger.On("Send", mock.Anything, session.ID, mock.MatchedBy(func(message string) bool {
				return strings.Contains(message, tc.commentURL) && strings.Contains(message, "discussion-1") && strings.Contains(message, "main.go:7")
			})).Return(nil)
			observation := ports.PRObservation{Fetched: true, URL: tc.commentURL, Number: 39, Review: domain.ReviewRequired}
			require.NoError(t, lifecycle.New(s, messenger).ApplyPRObservation(t.Context(), session.ID, observation))
			messenger.AssertNumberOfCalls(t, "Send", map[bool]int{false: 0, true: 1}[tc.wantPolicy])

			// A new lifecycle instance reads the persisted signature from SQLite.
			require.NoError(t, lifecycle.New(s, messenger).ApplyPRObservation(t.Context(), session.ID, observation))
			messenger.AssertNumberOfCalls(t, "Send", map[bool]int{false: 0, true: 1}[tc.wantPolicy])
		})
	}
}

type publicationMessenger struct{ mock.Mock }

func (m *publicationMessenger) Send(ctx context.Context, id domain.SessionID, message string) error {
	return m.Called(ctx, id, message).Error(0)
}
