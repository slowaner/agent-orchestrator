package scm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/gitlab"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// This boundary test starts at a fake GitLab discussions response, not at MCP
// publication. It exercises the real GitLab adapter, SCM observer, lifecycle
// reaction and worker messenger boundary without contacting a provider.
func TestGitLabDiffDiscussionReachesWorkerThroughObserver(t *testing.T) {
	for _, tc := range []struct {
		name         string
		wantLocation string
		author       string
		position     map[string]any
		individual   bool
		resolvable   bool
		resolved     bool
		inject       bool
		wantSend     bool
	}{
		{name: "new line", wantLocation: "src/new.go:12", position: map[string]any{"new_path": "src/new.go", "new_line": 12}, resolvable: true, inject: true, wantSend: true},
		{name: "deleted line", wantLocation: "src/old.go:12", position: map[string]any{"new_path": "src/new.go", "old_path": "src/old.go", "old_line": 12}, resolvable: true, inject: true, wantSend: true},
		{name: "self authored", wantLocation: "src/new.go:12", author: "alice", position: map[string]any{"new_path": "src/new.go", "new_line": 12}, resolvable: true, inject: true, wantSend: true},
		{name: "summary alone", individual: true, inject: true},
		{name: "resolved discussion", position: map[string]any{"new_path": "src/new.go", "new_line": 12}, resolvable: true, resolved: true, inject: true},
		{name: "auto inject disabled", position: map[string]any{"new_path": "src/new.go", "new_line": 12}, resolvable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// External URLs are SCM identity data; the injected client sends
			// every HTTP request to the loopback test server below.
			const prURL = "https://gitlab.com/group/sub/repo/-/merge_requests/39"
			const finding = "Keep the checked error <!-- ao-review run=gl-run head=gl-head finding=1 -->"
			author := tc.author
			if author == "" {
				author = "reviewer[bot]"
			}
			var mutations, discussionReads atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mutations.Add(1)
					http.Error(w, "read-only fixture", http.StatusMethodNotAllowed)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.EscapedPath() {
				case "/api/v4/projects/group%2Fsub%2Frepo/merge_requests/39/discussions":
					discussionReads.Add(1)
					_ = json.NewEncoder(w).Encode([]map[string]any{{
						"id": "discussion-1", "individual_note": tc.individual,
						"notes": []map[string]any{{
							"id": 40, "body": finding, "system": false,
							"resolvable": tc.resolvable, "resolved": tc.resolved,
							"author": map[string]any{"username": author}, "position": tc.position,
						}},
					}})
				case "/api/v4/projects/group%2Fsub%2Frepo/merge_requests/39/approvals":
					_, _ = w.Write([]byte(`{"approved":false,"approved_by":[]}`))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			gl, err := gitlab.NewProvider(gitlab.ProviderOptions{Client: gitlab.NewClient(gitlab.ClientOptions{
				RESTBase: server.URL + "/api/v4", Token: gitlab.StaticTokenSource("fixture-token"),
			})})
			require.NoError(t, err)
			repo, ok := gl.ParseRepository("https://gitlab.com/group/sub/repo.git")
			require.True(t, ok)

			now := time.Now().UTC()
			store := &feedbackStore{fakeStore: testStoreWithSession(), signatures: map[string]string{}}
			store.sessions[0].AutoInjectReview = tc.inject
			store.sessions[0].Activity = domain.Activity{State: domain.ActivityActive, LastActivityAt: now}
			store.projects["p"] = domain.ProjectRecord{ID: "p", RepoOriginURL: "https://gitlab.com/group/sub/repo.git"}
			metadata := ports.SCMObservation{
				Fetched: true, Provider: "gitlab", Host: "gitlab.com", Repo: "group/sub/repo",
				PR: ports.SCMPRObservation{URL: prURL, Number: 39, State: "open", SourceBranch: "feat", HeadSHA: "gl-head"},
			}
			local, _, _, _, _ := domainFromObservation("p-1", store.sessions[0], metadata, domain.PullRequest{}, persistenceOptions{}, now)
			local.MetadataHash = metadataSemanticHash(metadata)
			store.prs["p-1"] = []domain.PullRequest{local}
			provider := &feedbackProvider{
				fakeProvider: &fakeProvider{
					identity:     ports.SCMIdentity{Login: "alice", Human: true},
					repoGuards:   map[string]ports.SCMGuardResult{prKey(repo, 0): {ETag: "unchanged", NotModified: true}},
					observations: map[string]ports.SCMObservation{prKey(repo, 39): metadata},
				},
				gitlab: gl,
			}
			messenger := &feedbackMessenger{}
			manager := lifecycle.New(store, messenger)
			observer := New(provider, store, manager, Config{Clock: func() time.Time { return now }, Logger: quietSlog(), ScopedIdentityResolver: provider})
			observer.Cache.RepoPRListETag[prKey(repo, 0)] = "unchanged"

			require.NoError(t, observer.Poll(t.Context()))

			assert.Zero(t, mutations.Load())
			if !tc.wantSend {
				assert.Empty(t, messenger.messages)
				return
			}
			require.Len(t, messenger.messages, 1)
			assert.Equal(t, []domain.SessionID{"p-1"}, messenger.sessions)
			assert.Contains(t, messenger.messages[0], finding)
			assert.Contains(t, messenger.messages[0], "discussion-1")
			assert.Contains(t, messenger.messages[0], prURL+"#note_40")
			assert.Contains(t, messenger.messages[0], tc.wantLocation)
			require.Len(t, store.comments, 1)
			assert.Empty(t, store.comments[0].ReviewID, "GitLab comment IDs are not GitHub review IDs")

			// Reload committed fake-store rows into fresh observer/lifecycle
			// instances. Advance the clock and the provider's update timestamp
			// so lifecycle must reconsider the persisted finding, not skip it
			// because of the review refresh cadence or unchanged hashes.
			require.Equal(t, int64(1), discussionReads.Load())
			require.NotEmpty(t, store.signatures[prURL])
			reloaded := &feedbackStore{
				fakeStore: &fakeStore{
					sessions: append([]domain.SessionRecord(nil), store.sessions...),
					projects: store.projects,
					prs:      map[domain.SessionID][]domain.PullRequest{"p-1": append([]domain.PullRequest(nil), store.prs["p-1"]...)},
					checks:   store.checks,
				},
				comments:   append([]domain.PullRequestComment(nil), store.comments...),
				reviews:    append([]domain.PullRequestReview(nil), store.reviews...),
				signatures: map[string]string{prURL: store.signatures[prURL]},
			}
			now = now.Add(DefaultPRMaxAge + DefaultReviewInterval + time.Second)
			metadata.PR.Title = "Retitled after review"
			metadata.PR.UpdatedAtProvider = now
			provider.observations[prKey(repo, 39)] = metadata
			restartedMessenger := &feedbackMessenger{}
			restartedManager := lifecycle.New(reloaded, restartedMessenger)
			restartedObserver := New(provider, reloaded, restartedManager, Config{Clock: func() time.Time { return now }, Logger: quietSlog(), ScopedIdentityResolver: provider})

			require.NoError(t, restartedObserver.Poll(t.Context()))

			assert.Equal(t, int64(2), discussionReads.Load(), "the restarted observer must fetch discussions again")
			assert.Greater(t, reloaded.signatureReads, 0, "lifecycle must load persisted delivery signatures")
			assert.Empty(t, restartedMessenger.messages, "the already delivered finding must not be sent again")
			assert.Zero(t, mutations.Load())

		})
	}
}

type feedbackProvider struct {
	*fakeProvider
	gitlab *gitlab.Provider
}

func (p *feedbackProvider) ParseRepository(remote string) (ports.SCMRepo, bool) {
	return p.gitlab.ParseRepository(remote)
}

func (p *feedbackProvider) FetchReviewThreads(ctx context.Context, ref ports.SCMPRRef) (ports.SCMReviewObservation, error) {
	return p.gitlab.FetchReviewThreads(ctx, ref)
}

type feedbackMessenger struct {
	sessions []domain.SessionID
	messages []string
}

func (m *feedbackMessenger) Send(_ context.Context, id domain.SessionID, text string) error {
	m.sessions = append(m.sessions, id)
	m.messages = append(m.messages, text)
	return nil
}

// feedbackStore shares committed observer rows with the real lifecycle reducer.
type feedbackStore struct {
	signatureReads int
	*fakeStore
	comments   []domain.PullRequestComment
	reviews    []domain.PullRequestReview
	signatures map[string]string
}

func (s *feedbackStore) WriteSCMObservation(ctx context.Context, pr domain.PullRequest, checks []domain.PullRequestCheck, reviews []domain.PullRequestReview, threads []domain.PullRequestReviewThread, comments []domain.PullRequestComment, mode ports.ReviewWriteMode) error {
	if err := s.fakeStore.WriteSCMObservation(ctx, pr, checks, reviews, threads, comments, mode); err != nil {
		return err
	}
	s.prs[pr.SessionID] = []domain.PullRequest{pr}
	if mode != ports.ReviewWritePreserve {
		s.comments = comments
		s.reviews = reviews
	}
	return nil
}

func (s *feedbackStore) GetSession(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	for _, session := range s.sessions {
		if session.ID == id {
			return session, true, nil
		}
	}
	return domain.SessionRecord{}, false, nil
}

func (s *feedbackStore) UpdateSession(_ context.Context, record domain.SessionRecord) error {
	for i := range s.sessions {
		if s.sessions[i].ID == record.ID {
			s.sessions[i] = record
		}
	}
	return nil
}

func (s *feedbackStore) UpdateSessionFromActivitySignal(ctx context.Context, record domain.SessionRecord, revision int64) (bool, error) {
	old, found, err := s.GetSession(ctx, record.ID)
	if err != nil || !found || old.Revision != revision {
		return false, err
	}
	record.Revision++
	return true, s.UpdateSession(ctx, record)
}

func (s *feedbackStore) UpdateSessionArtifactOutput(ctx context.Context, id domain.SessionID, artifactDir string, outputType domain.SessionOutputType) (bool, error) {
	record, found, err := s.GetSession(ctx, id)
	if err != nil || !found {
		return false, err
	}
	record.Metadata.ArtifactDir, record.OutputType = artifactDir, outputType
	return true, s.UpdateSession(ctx, record)
}

func (s *feedbackStore) ListSessions(_ context.Context, project domain.ProjectID) ([]domain.SessionRecord, error) {
	var records []domain.SessionRecord
	for _, session := range s.sessions {
		if session.ProjectID == project {
			records = append(records, session)
		}
	}
	return records, nil
}

func (s *feedbackStore) GetPR(_ context.Context, url string) (domain.PullRequest, bool, error) {
	for _, prs := range s.prs {
		for _, pr := range prs {
			if pr.URL == url {
				return pr, true, nil
			}
		}
	}
	return domain.PullRequest{}, false, nil
}

func (s *feedbackStore) ListPRReviews(context.Context, string) ([]domain.PullRequestReview, error) {
	return s.reviews, nil
}

func (s *feedbackStore) ListPRComments(context.Context, string) ([]domain.PullRequestComment, error) {
	return s.comments, nil
}

func (s *feedbackStore) GetPRLastNudgeSignature(_ context.Context, url string) (string, error) {
	s.signatureReads++
	return s.signatures[url], nil
}

func (s *feedbackStore) UpdatePRLastNudgeSignature(_ context.Context, url, signature string) error {
	s.signatures[url] = signature
	return nil
}
