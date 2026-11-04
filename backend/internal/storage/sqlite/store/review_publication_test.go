package store_test

import (
	"cmp"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/scm"
	reviewsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/review"
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

func TestReceiptRefreshReconsidersUnchangedStoredFinding(t *testing.T) {
	const target = "https://gitlab.example/group/sub/repo/-/merge_requests/39"
	const head = "0123456789012345678901234567890123456789"
	for _, tc := range []struct {
		name, status, publicationError string
		enabledAtSubmit                bool
		firstSendError                 error
		wantFirstSends                 int
		wantAfterRetry                 int
		requestWorker, requestHead     string
		wantSends                      int
	}{
		{name: "published enabled snapshot", status: "published", enabledAtSubmit: true, wantFirstSends: 1, wantAfterRetry: 1, wantSends: 1},
		{name: "published disabled snapshot stays disabled", status: "published"},
		{name: "wrong worker request", status: "published", enabledAtSubmit: true, requestWorker: "other", wantSends: 1},
		{name: "wrong head request", status: "published", enabledAtSubmit: true, requestHead: "other", wantSends: 1},
		{name: "failed lifecycle retries existing request", status: "published", enabledAtSubmit: true, firstSendError: errors.New("local delivery failed"), wantFirstSends: 1, wantAfterRetry: 2, wantSends: 2},
		{name: "blocked receipt", status: "blocked", publicationError: "denied", enabledAtSubmit: true},
		{name: "unconfirmed receipt", status: "unconfirmed", publicationError: "unknown outcome", enabledAtSubmit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			now := time.Now().UTC().Truncate(time.Second)
			require.NoError(t, s.UpsertProject(t.Context(), domain.ProjectRecord{ID: "mer", Path: "/tmp/mer", RepoOriginURL: "https://gitlab.example/group/sub/repo.git", RegisteredAt: now}))
			record := sampleRecord("mer")
			record.AutoInjectReview = false
			session, err := s.CreateSession(t.Context(), record)
			require.NoError(t, err)
			require.NoError(t, s.UpsertReview(t.Context(), domain.Review{ID: "review", SessionID: session.ID, ProjectID: "mer", Harness: "claude-code", PRURL: target, CreatedAt: now, UpdatedAt: now}))
			require.NoError(t, s.InsertReviewRun(t.Context(), domain.ReviewRun{ID: "run", ReviewID: "review", SessionID: session.ID, Harness: "claude-code", PRURL: target, TargetSHA: head, Status: domain.ReviewRunRunning, CreatedAt: now}))
			require.NoError(t, s.WriteSCMObservation(t.Context(), domain.PullRequest{URL: target, SessionID: session.ID, Number: 39, HeadSHA: head, UpdatedAt: now, ObservedAt: now}, nil, nil, nil, nil, ports.ReviewWritePreserve))

			repo := ports.SCMRepo{Provider: "gitlab", Host: "gitlab.example", Owner: "group", Name: "sub/repo", Repo: "group/sub/repo"}
			ref := ports.SCMPRRef{Repo: repo, Number: 39, URL: target}
			observation := ports.SCMObservation{Fetched: true, Provider: "gitlab", Host: repo.Host, Repo: repo.Repo, PR: ports.SCMPRObservation{URL: target, Number: 39, State: "open", SourceBranch: record.Metadata.Branch, HeadSHA: head}}
			review := ports.SCMReviewObservation{Decision: string(domain.ReviewRequired), Threads: []ports.SCMReviewThreadObservation{{ID: "discussion-1", Path: "main.go", Line: 7, Comments: []ports.SCMReviewCommentObservation{{ID: "note-1", Author: "alice", Body: "Handle the error", URL: target + "#note_1"}}}}}
			provider := &publicationSCM{}
			provider.Test(t)
			provider.On("ParseRepository", "https://gitlab.example/group/sub/repo.git").Return(repo, true)
			provider.On("AuthenticatedIdentityForProvider", mock.Anything, "gitlab", repo.Host).Return(ports.SCMIdentity{Login: "alice", Human: true}, nil)
			provider.On("RepoPRListGuard", mock.Anything, repo, mock.Anything).Return(ports.SCMGuardResult{NotModified: true}, nil)
			provider.On("ListPRsByRepo", mock.Anything, repo, mock.Anything).Return([]ports.SCMPRObservation{observation.PR}, nil)
			provider.On("CommitChecksGuard", mock.Anything, repo, head, mock.Anything).Return(ports.SCMGuardResult{NotModified: true}, nil)
			provider.On("FetchPullRequests", mock.Anything, []ports.SCMPRRef{ref}).Return([]ports.SCMObservation{observation}, nil)
			provider.On("FetchReviewThreads", mock.Anything, ref).Return(review, nil)

			messenger := &publicationMessenger{}
			messenger.Test(t)
			messenger.On("Send", mock.Anything, session.ID, mock.MatchedBy(func(text string) bool {
				return strings.Contains(text, target+"#note_1") && strings.Contains(text, "discussion-1") && strings.Contains(text, "main.go:7")
			})).Return(tc.firstSendError).Once()
			messenger.On("Send", mock.Anything, session.ID, mock.MatchedBy(func(text string) bool {
				return strings.Contains(text, target+"#note_1") && strings.Contains(text, "discussion-1") && strings.Contains(text, "main.go:7")
			})).Return(nil)
			manager := lifecycle.New(s, messenger)
			observer := scm.New(provider, s, manager, scm.Config{Clock: func() time.Time { return now }, ScopedIdentityResolver: provider})

			require.NoError(t, observer.Poll(t.Context()))
			provider.AssertNumberOfCalls(t, "FetchReviewThreads", 1)
			feedback, err := s.ListPRComments(t.Context(), target)
			require.NoError(t, err)
			require.Len(t, feedback, 1)
			require.False(t, feedback[0].AutoInjectReview)
			messenger.AssertNumberOfCalls(t, "Send", 0)
			before, err := s.ListPRsBySession(t.Context(), session.ID)
			require.NoError(t, err)
			require.Len(t, before, 1)

			// The immutable run snapshot is first recorded by SubmitMany using
			// the session's policy at submission, not by rewriting a false result.
			_, err = s.SetSessionAutoInjectReview(t.Context(), session.ID, tc.enabledAtSubmit, now)
			require.NoError(t, err)
			submitted := []reviewsvc.SubmittedReview{{RunID: "run", Verdict: domain.VerdictChangesRequested, Body: "Handle the error", Publication: &domain.ReviewPublication{Provider: "gitlab", Status: tc.status, PRURL: target, TargetSHA: head, Error: tc.publicationError, Refs: []domain.ReviewPublicationRef{{Kind: "discussion", ID: "discussion-1"}, {Kind: "note", ID: "note-1"}, {Kind: "note", ID: "summary-1"}}}}}
			service := reviewsvc.New(nil, s, reviewsvc.WithClock(func() time.Time { return now }), reviewsvc.WithThreadRefresher(observer))
			runs, err := service.SubmitMany(t.Context(), session.ID, submitted)
			require.NoError(t, err)
			require.Len(t, runs, 1)
			require.Equal(t, tc.enabledAtSubmit, runs[0].AutoInjectReview)
			_, err = s.SetSessionAutoInjectReview(t.Context(), session.ID, true, now)
			require.NoError(t, err)

			observer.RequestReviewRefresh(t.Context(), ports.ReviewRefreshRequest{RunID: "run", WorkerID: domain.SessionID(cmp.Or(tc.requestWorker, string(session.ID))), PRURL: target, TargetSHA: cmp.Or(tc.requestHead, head)})
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			// A cancelled request cannot replace the current bound or invalid token.
			observer.RequestReviewRefresh(cancelled, ports.ReviewRefreshRequest{RunID: "run", WorkerID: session.ID, PRURL: target, TargetSHA: head})
			require.NoError(t, observer.Poll(t.Context()))
			messenger.AssertNumberOfCalls(t, "Send", tc.wantFirstSends)
			// A failed local send keeps its exact refresh request for the next
			// poll. Successful delivery leaves an ordinary unchanged timer idle.
			require.NoError(t, observer.Poll(t.Context()))
			messenger.AssertNumberOfCalls(t, "Send", tc.wantAfterRetry)
			after, err := s.ListPRsBySession(t.Context(), session.ID)
			require.NoError(t, err)
			require.Len(t, after, 1)
			require.Equal(t, before[0].MetadataHash, after[0].MetadataHash)
			require.Equal(t, before[0].ReviewHash, after[0].ReviewHash)
			require.Equal(t, before[0].CIHash, after[0].CIHash)

			// Receipt replay preserves the snapshot. A duplicate forced refresh
			// and a cold lifecycle read must not send an already-seen finding.
			_, err = service.SubmitMany(t.Context(), session.ID, submitted)
			require.NoError(t, err)
			observer.RequestReviewRefresh(t.Context(), ports.ReviewRefreshRequest{RunID: "run", WorkerID: session.ID, PRURL: target, TargetSHA: head})
			require.NoError(t, observer.Poll(t.Context()))
			signature, err := s.GetPRLastNudgeSignature(t.Context(), target)
			require.NoError(t, err)
			require.Equal(t, tc.wantSends > 0, signature != "")
			require.NoError(t, lifecycle.New(s, messenger).ApplyPRObservation(t.Context(), session.ID, ports.PRObservation{Fetched: true, URL: target, Number: 39, Review: domain.ReviewRequired}))
			restoredSignature, err := s.GetPRLastNudgeSignature(t.Context(), target)
			require.NoError(t, err)
			require.Equal(t, signature, restoredSignature)
			messenger.AssertNumberOfCalls(t, "Send", tc.wantSends)
			run, found, err := s.GetReviewRun(t.Context(), "run")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, tc.enabledAtSubmit, run.AutoInjectReview)
			provider.AssertExpectations(t)
		})
	}
}

type publicationMessenger struct{ mock.Mock }
type publicationSCM struct{ mock.Mock }

func (m *publicationSCM) ParseRepository(remote string) (ports.SCMRepo, bool) {
	a := m.Called(remote)
	return a.Get(0).(ports.SCMRepo), a.Bool(1)
}

func (m *publicationSCM) AuthenticatedIdentityForProvider(ctx context.Context, provider, host string) (ports.SCMIdentity, error) {
	a := m.Called(ctx, provider, host)
	return a.Get(0).(ports.SCMIdentity), a.Error(1)
}

func (m *publicationSCM) RepoPRListGuard(ctx context.Context, repo ports.SCMRepo, etag string) (ports.SCMGuardResult, error) {
	a := m.Called(ctx, repo, etag)
	return a.Get(0).(ports.SCMGuardResult), a.Error(1)
}

func (m *publicationSCM) CommitChecksGuard(ctx context.Context, repo ports.SCMRepo, head, etag string) (ports.SCMGuardResult, error) {
	a := m.Called(ctx, repo, head, etag)
	return a.Get(0).(ports.SCMGuardResult), a.Error(1)
}

func (m *publicationSCM) ListPRsByRepo(ctx context.Context, repo ports.SCMRepo, after time.Time) ([]ports.SCMPRObservation, error) {
	a := m.Called(ctx, repo, after)
	return a.Get(0).([]ports.SCMPRObservation), a.Error(1)
}

func (m *publicationSCM) FetchPullRequests(ctx context.Context, refs []ports.SCMPRRef) ([]ports.SCMObservation, error) {
	a := m.Called(ctx, refs)
	return a.Get(0).([]ports.SCMObservation), a.Error(1)
}

func (m *publicationSCM) FetchReviewThreads(ctx context.Context, ref ports.SCMPRRef) (ports.SCMReviewObservation, error) {
	a := m.Called(ctx, ref)
	return a.Get(0).(ports.SCMReviewObservation), a.Error(1)
}

func (m *publicationSCM) FetchFailedCheckLogTail(ctx context.Context, repo ports.SCMRepo, check ports.SCMCheckObservation) (string, error) {
	a := m.Called(ctx, repo, check)
	return a.String(0), a.Error(1)
}

func (m *publicationMessenger) Send(ctx context.Context, id domain.SessionID, message string) error {
	return m.Called(ctx, id, message).Error(0)
}
