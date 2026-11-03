//go:build e2e_test

package reviewapproval

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	worker "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/codex"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/processenv"
	chatregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/registry"
	reviewregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/reviewer"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/review"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var (
	liveProbe = flag.Bool("reviewapproval.live", false, "run the separately authorized native approval scenario")
	labRoot   = flag.String("reviewapproval.lab-root", "", "new private directory directly under the user's .ao/labs")
	aoBinary  = flag.String("reviewapproval.ao-binary", "", "existing AO binary inside the owned lab directory")
)

// These are binding data, not HTTP request destinations. This scenario must
// never invoke GitLab; its only permitted native command prints a run marker.
const (
	fixtureURL  = "https://gitlab.ptsecurity.com/nturchaninov/ao-testing/-/merge_requests/1"
	fixtureHead = "3133da21de25c78fa622668ceb60177e04ee304e"
)

func TestProbeChatOptionsAllocateConversationAndRejectUnavailableDriver(t *testing.T) {
	st, cfg := probeBindingFixture(t)
	opts := probeChatOptions(st, chatregistry.New(), t.TempDir())
	require.NotNil(t, opts.NewID)

	firstID, secondID := opts.NewID(), opts.NewID()
	require.NotEmpty(t, firstID)
	require.NotEqual(t, firstID, secondID)
	conversation, err := opts.Store.CreateReviewConversation(t.Context(), firstID, cfg.ReviewID, cfg.ProjectID, cfg.WorkerID, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, firstID, conversation.ID)
	rows, err := opts.Reader.LoadConversationSnapshot(t.Context(), conversation.ID)
	require.NoError(t, err)
	require.Equal(t, conversation.ID, rows.Conversation.ID)

	// The real empty registry fails admission before any provider process.
	// This exercises constructor ports without a fake driver or model call.
	service := chatsvc.New(opts)
	_, err = service.StartChat(t.Context(), chatsvc.StartConfig{
		Owner: domain.ReviewConversationOwner(cfg.ReviewID), SessionID: cfg.WorkerID,
		ProjectID: cfg.ProjectID, Harness: domain.HarnessCodex, DataDir: opts.DataDir,
		WorkspacePath: t.TempDir(), ReviewerApproval: cfg.ReviewerApproval,
	})
	require.ErrorIs(t, err, ports.ErrChatUnsupported)
}

func TestProbeObservedFixtureRetainsEligibilityFacts(t *testing.T) {
	st, cfg := probeBindingFixture(t)

	prs, err := st.ListPRsBySession(t.Context(), cfg.WorkerID)

	require.NoError(t, err)
	require.Len(t, prs, 1)
	require.Equal(t, fixtureURL, prs[0].URL)
	require.Equal(t, fixtureHead, prs[0].HeadSHA)
	// A first trigger has no earlier runs. Real persisted target facts must
	// reach the production planner instead of producing an ineligible target.
	planned := review.Plan(prs, nil)
	require.Len(t, planned, 1)
	require.Equal(t, review.ReviewStateNeedsReview, planned[0].Status)
}

func TestProbeFirstMessageIsBoundBeforeSend(t *testing.T) {
	st, cfg := probeBindingFixture(t)
	bridge := probeBridge{store: st}

	prepared, err := bridge.prepareStart(t.Context(), cfg, false)

	require.NoError(t, err)
	require.Equal(t, cfg.SystemPrompt, prepared.SystemPrompt)
	require.Equal(t, cfg.Env, prepared.Env)
	require.Equal(t, cfg.ReviewerApproval, prepared.ReviewerApproval)
	require.Contains(t, prepared.Prompt, probeCommand(cfg.ReviewerApproval.RunID))
	require.NotContains(t, prepared.Prompt, cfg.Prompt)
}

func TestProbeDeferredMessageIsBoundBeforeSend(t *testing.T) {
	st, cfg := probeBindingFixture(t)
	bridge := probeBridge{store: st}
	cfg.Prompt = ""

	prepared, err := bridge.prepareStart(t.Context(), cfg, false)
	require.NoError(t, err)
	require.Empty(t, prepared.Prompt, "a deferred start must not start a turn")

	message, err := bridge.prepareNotify(t.Context(), cfg.ReviewID, cfg.BatchID)

	require.NoError(t, err)
	require.Contains(t, message, probeCommand(cfg.ReviewerApproval.RunID))
}

func TestProbeRestoreRemainsSilentAfterCancellation(t *testing.T) {
	st, cfg := probeBindingFixture(t)
	_, err := st.CancelRunningReviewRunsBySession(t.Context(), cfg.WorkerID, "local binding fixture")
	require.NoError(t, err)
	bridge := probeBridge{store: st}
	cfg.ReviewerApproval = nil

	prepared, err := bridge.prepareStart(t.Context(), cfg, true)

	require.NoError(t, err)
	require.Empty(t, prepared.Prompt)
	require.Equal(t, cfg.SystemPrompt, prepared.SystemPrompt)
	require.Nil(t, prepared.ReviewerApproval)
}

func TestProbeRejectsMismatchedRun(t *testing.T) {
	st, cfg := probeBindingFixture(t)
	bridge := probeBridge{store: st}
	wrong := *cfg.ReviewerApproval
	wrong.TargetSHA = strings.Repeat("0", 40)
	cfg.ReviewerApproval = &wrong

	_, err := bridge.prepareStart(t.Context(), cfg, false)
	require.ErrorContains(t, err, "binding")
}

func TestProbeRejectsCancelledRunOnDeferredSend(t *testing.T) {
	st, cfg := probeBindingFixture(t)
	bridge := probeBridge{store: st}
	cfg.Prompt = ""
	_, err := bridge.prepareStart(t.Context(), cfg, false)
	require.NoError(t, err)

	_, err = st.CancelRunningReviewRunsBySession(t.Context(), cfg.WorkerID, "local binding fixture")
	require.NoError(t, err)
	_, err = bridge.prepareNotify(t.Context(), cfg.ReviewID, cfg.BatchID)
	require.ErrorContains(t, err, "running")
}

func TestProbeApprovalRejectsExtraAuthorityOrDifferentCommand(t *testing.T) {
	_, cfg := probeBindingFixture(t)
	scope := *cfg.ReviewerApproval
	snapshot := chatsvc.Snapshot{
		ActiveBranch: domain.ConversationBranch{ProviderConversationID: "native-thread"},
		Turns:        []domain.ConversationTurn{{ID: "local-turn", HandledByReviewID: scope.ReviewID, ProviderTurnID: "native-turn"}},
	}
	detail := map[string]any{
		"method": "item/commandExecution/requestApproval", "command": probeCommand(scope.RunID),
		"cwd": "/owned-workspace", "reviewerScope": scope,
		"decisions": []map[string]string{{"id": "accept"}, {"id": "decline"}, {"id": "cancel"}},
	}
	encoded, err := json.Marshal(detail)
	require.NoError(t, err)
	activity := domain.ConversationActivity{Kind: domain.ActivityKindApproval, Status: domain.ActivityStatusPending, RequestID: "review:epoch:" + scope.RunID + ":1", TurnID: "local-turn", Detail: encoded}
	require.NoError(t, validateProbeApproval(snapshot, activity, scope, "/owned-workspace"))

	detail["command"] = probeCommand(scope.RunID) + "; git status"
	activity.Detail, err = json.Marshal(detail)
	require.NoError(t, err)
	require.Error(t, validateProbeApproval(snapshot, activity, scope, "/owned-workspace"))

	detail["command"] = probeCommand(scope.RunID)
	detail["decisions"] = []map[string]string{{"id": "accept"}, {"id": "acceptForSession"}}
	activity.Detail, err = json.Marshal(detail)
	require.NoError(t, err)
	require.Error(t, validateProbeApproval(snapshot, activity, scope, "/owned-workspace"))

	detail["decisions"] = []map[string]string{{"id": "accept"}}
	detail["additionalPermissions"] = map[string]bool{"network": true}
	activity.Detail, err = json.Marshal(detail)
	require.NoError(t, err)
	require.Error(t, validateProbeApproval(snapshot, activity, scope, "/owned-workspace"))
}

func TestProbeApprovalHTTPRejectsAnotherOwnerAndPersistentDecision(t *testing.T) {
	scope := &ports.ReviewerApprovalScope{ReviewID: "owned-review"}
	handler := probeApprovalHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("an out-of-envelope request reached the production resolver")
	}), nil, scope, "owned-request", "/owned-workspace")
	other := httptest.NewRecorder()
	handler.ServeHTTP(other, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v1/reviews/other/conversation/approvals/owned-request/resolve", strings.NewReader(`{"decisionId":"accept"}`)))
	require.Equal(t, http.StatusNotFound, other.Code)

	persistent := httptest.NewRecorder()
	handler.ServeHTTP(persistent, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v1/reviews/owned-review/conversation/approvals/owned-request/resolve", strings.NewReader(`{"decisionId":"acceptForSession"}`)))
	require.Equal(t, http.StatusBadRequest, persistent.Code)
}

func TestProbeHostConfigUsesProductionOwnerAndRawProtocol(t *testing.T) {
	start := review.ReviewerChatStart{ReviewID: "owned-review", DataDir: "/owned-lab/data", WorkspacePath: "/owned-lab/workspace"}
	env := []string{"AO_DATA_DIR=/owned-lab/data", "BOUND_CONTEXT=explicit"}

	cfg := probeHostConfig(start, "/existing-codex", env)

	require.Equal(t, "review-owned-review", cfg.SessionID)
	require.Equal(t, start.DataDir, cfg.DataDir)
	require.Equal(t, start.WorkspacePath, cfg.Workdir)
	require.Equal(t, env, cfg.Env)
	require.Equal(t, []string{"/existing-codex", "app-server"}, cfg.Argv)
	require.Equal(t, persistenthost.ProtocolRaw, cfg.Protocol)
	require.Empty(t, cfg.OwnershipFingerprint)
	require.False(t, cfg.ReconnectOnly)
}

func TestNativeReviewerCancelRestoreReplay(t *testing.T) {
	if !*liveProbe {
		t.Skip("native/model operations require a separately reserved live allocation")
	}
	require.NoError(t, validateLabPaths(*labRoot, *aoBinary))
	rootInfo, err := os.Stat(*labRoot)
	require.NoError(t, err, "prepare only this lab's source/binary before the live allocation")
	require.True(t, rootInfo.IsDir())
	workspace := filepath.Join(*labRoot, "workspace")
	require.NoError(t, os.Mkdir(workspace, 0o700), "never reuse an existing runtime workspace")
	dataDir := filepath.Join(*labRoot, "data")
	_, err = os.Lstat(dataDir)
	require.ErrorIs(t, err, os.ErrNotExist, "never reuse another runtime's database")

	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	host := &probeNativeHost{ctx: ctx}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer done()
		require.NoError(t, host.stop(cleanup))
	})
	projectID := domain.ProjectID("approval-probe-" + uuid.NewString())
	workerID := domain.SessionID(string(projectID) + "-1")
	st, svc, bridge, engine := openNativeProbe(t, dataDir, *aoBinary, host)
	// Cleanup follows the current generation, including after the owned restart.
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer done()
		if bridge.reviewID != "" {
			_ = svc.StopForOwner(cleanup, domain.ReviewConversationOwner(bridge.reviewID))
		}
		svc.StopAll(cleanup)
		require.NoError(t, st.Close())
	})
	now := time.Now().UTC()
	require.NoError(t, st.UpsertProject(ctx, domain.ProjectRecord{ID: string(projectID), Path: workspace, RegisteredAt: now}))
	createdWorker, err := st.CreateSession(ctx, domain.SessionRecord{
		ID: workerID, ProjectID: projectID, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, ReviewerHarness: domain.ReviewerCodex,
		Mode: domain.SessionModeChat, CreatedAt: now, UpdatedAt: now,
		Metadata: domain.SessionMetadata{WorkspacePath: workspace},
	})
	require.NoError(t, err)
	workerID = createdWorker.ID
	// A normal observed fixture PR supplies the engine's target. Approval/run
	// rows below are created only by the production trigger and native events.
	require.NoError(t, writeProbeTarget(ctx, st, workerID, now))
	opts := review.TriggerOptions{Harness: domain.ReviewerCodex, InterfaceMode: domain.ReviewerInterfaceChat, PRURL: fixtureURL, PublicationRoute: domain.ReviewPublicationMCP, Rerun: true}

	first, err := engine.TriggerWithOptions(ctx, workerID, opts)
	require.NoError(t, err)
	require.True(t, first.Created)
	require.Len(t, first.CreatedRuns, 1)
	firstScope := scopeForRun(first.Run)
	firstSnapshot, firstRequest := waitProbePending(t, ctx, svc, firstScope, workspace)
	logProbePending(t, "cancel", "", firstSnapshot, firstRequest, firstScope)
	require.Zero(t, completedProbeCommands(firstSnapshot, firstScope.RunID))
	require.Zero(t, probeExecutionCount(firstSnapshot, firstScope.RunID))

	_, err = engine.Cancel(ctx, workerID)
	require.NoError(t, err)
	settled := waitProbeTerminal(t, ctx, svc, firstScope.ReviewID, firstRequest.TurnID, domain.TurnStateInterrupted)
	require.Zero(t, completedProbeCommands(settled, firstScope.RunID))
	require.Zero(t, probeExecutionCount(settled, firstScope.RunID))
	require.NoError(t, requireNoPending(settled))
	oldNativeID := settled.ActiveBranch.ProviderConversationID
	require.NotEmpty(t, oldNativeID)
	oldController, err := svc.ControllerForOwner(domain.ReviewConversationOwner(firstScope.ReviewID))
	require.NoError(t, err)
	stopCtx, stopDone := context.WithTimeout(ctx, 30*time.Second)
	svc.StopAll(stopCtx)
	stopDone()
	require.Equal(t, ports.ChatControllerStopped, oldController.State())
	require.NoError(t, st.Close())

	st, svc, bridge, engine = openNativeProbe(t, dataDir, *aoBinary, host)
	_, err = engine.RestoreReviewer(ctx, workerID)
	require.NoError(t, err)
	restored, err := svc.SnapshotForReview(ctx, firstScope.ReviewID)
	require.NoError(t, err)
	require.Equal(t, oldNativeID, restored.ActiveBranch.ProviderConversationID)
	require.NoError(t, requireNoPending(restored))
	require.Zero(t, completedProbeCommands(restored, firstScope.RunID))
	require.Zero(t, probeExecutionCount(restored, firstScope.RunID))
	err = svc.ResolveForOwner(ctx, domain.ReviewConversationOwner(firstScope.ReviewID), firstRequest.RequestID, ports.ChatDecision{ID: "accept"})
	require.ErrorIs(t, err, ports.ErrChatRequestNotPending)
	t.Logf("NATIVE_STALE_REJECTED phase=restored run=%s request=%s", firstScope.RunID, firstRequest.RequestID)

	second, err := engine.TriggerWithOptions(ctx, workerID, opts)
	require.NoError(t, err)
	require.True(t, second.Created)
	require.Len(t, second.CreatedRuns, 1)
	secondScope := scopeForRun(second.Run)
	require.NotEqual(t, firstScope.RunID, secondScope.RunID)
	current, currentRequest := waitProbePending(t, ctx, svc, secondScope, workspace)
	err = svc.ResolveForOwner(ctx, domain.ReviewConversationOwner(secondScope.ReviewID), firstRequest.RequestID, ports.ChatDecision{ID: "accept"})
	require.ErrorIs(t, err, ports.ErrChatRequestNotPending)
	afterReplay, err := svc.SnapshotForReview(ctx, secondScope.ReviewID)
	require.NoError(t, err)
	require.True(t, requestPending(afterReplay, currentRequest.RequestID), "stale replay must preserve the current human request")
	require.Zero(t, completedProbeCommands(afterReplay, secondScope.RunID))

	// Only the coordinator/human may resolve this actual current request. This
	// server mounts the existing production resolver; the test never accepts it.
	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) { (&controllers.ConversationsController{Svc: svc}).Register(r) })
	server := httptest.NewServer(probeApprovalHandler(router, svc, secondScope, currentRequest.RequestID, workspace))
	t.Cleanup(server.Close)
	logProbePending(t, "human", server.URL, current, currentRequest, secondScope)
	completed := waitProbeTerminal(t, ctx, svc, secondScope.ReviewID, currentRequest.TurnID, domain.TurnStateCompleted)
	require.Equal(t, 1, completedProbeCommands(completed, secondScope.RunID), "require real command output and exit0, not model prose")
	require.Equal(t, 1, probeExecutionCount(completed, secondScope.RunID), "failed or repeated executions must not disappear from the once-only proof")
	require.Zero(t, completedProbeCommands(completed, firstScope.RunID))
	require.Zero(t, probeExecutionCount(completed, firstScope.RunID))
	require.NoError(t, requireNoPending(completed))
	_, err = engine.Cancel(ctx, workerID)
	require.NoError(t, err)
	for _, runID := range []string{firstScope.RunID, secondScope.RunID} {
		run, found, err := st.GetReviewRun(ctx, runID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, domain.ReviewRunCancelled, run.Status)
		require.Nil(t, run.Publication)
		require.Empty(t, run.GithubReviewID)
		require.Nil(t, run.DeliveredAt)
	}
	t.Logf("NATIVE_CANCEL_RESTORE_REPLAY_PASS first_run=%s second_run=%s native_thread=%s", firstScope.RunID, secondScope.RunID, oldNativeID)
}

func probeBindingFixture(t *testing.T) (*sqlite.Store, review.ReviewerChatStart) {
	t.Helper()
	st, err := sqlite.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	now := time.Now().UTC()
	projectID := domain.ProjectID(uuid.NewString())
	workerID := domain.SessionID(uuid.NewString())
	require.NoError(t, st.UpsertProject(t.Context(), domain.ProjectRecord{ID: string(projectID), RegisteredAt: now}))
	createdWorker, err := st.CreateSession(t.Context(), domain.SessionRecord{ID: workerID, ProjectID: projectID, Kind: domain.KindWorker, Harness: domain.HarnessCodex, CreatedAt: now, UpdatedAt: now})
	require.NoError(t, err)
	workerID = createdWorker.ID
	reviewID := uuid.NewString()
	require.NoError(t, st.UpsertReview(t.Context(), domain.Review{ID: reviewID, SessionID: workerID, ProjectID: projectID, Harness: domain.ReviewerCodex, CreatedAt: now, UpdatedAt: now}))
	run := domain.ReviewRun{ID: uuid.NewString(), ReviewID: reviewID, SessionID: workerID, BatchID: uuid.NewString(), Harness: domain.ReviewerCodex, PRURL: fixtureURL, TargetSHA: fixtureHead, Status: domain.ReviewRunRunning, CreatedAt: now}
	require.NoError(t, writeProbeTarget(t.Context(), st, workerID, now))
	require.NoError(t, st.InsertReviewRun(t.Context(), run))
	return st, review.ReviewerChatStart{ReviewerApproval: scopeForRun(run), ReviewID: reviewID, WorkerID: workerID, ProjectID: projectID, BatchID: run.BatchID, Harness: domain.HarnessCodex, Prompt: "original review task reference", SystemPrompt: "unchanged system instructions", Env: map[string]string{"AO_DATA_DIR": "unchanged-overlay"}}
}

func writeProbeTarget(ctx context.Context, st *sqlite.Store, workerID domain.SessionID, now time.Time) error {
	return st.WriteSCMObservation(ctx, domain.PullRequest{
		URL: fixtureURL, HTMLURL: fixtureURL, SessionID: workerID, Number: 1,
		Provider: "gitlab", Host: "gitlab.ptsecurity.com", Repo: "nturchaninov/ao-testing",
		ProviderID: "654310", HeadSHA: fixtureHead, Draft: true, UpdatedAt: now, ObservedAt: now,
	}, nil, nil, nil, nil, ports.ReviewWritePreserve)
}

type probeBridge struct {
	store    *sqlite.Store
	service  *chatsvc.Service
	reviewID string
	scope    *ports.ReviewerApprovalScope
	host     *probeNativeHost
}

var _ review.ReviewerChatController = (*probeBridge)(nil)

func (*probeBridge) SupportsReviewChat(h domain.AgentHarness) bool { return h == domain.HarnessCodex }

func (b *probeBridge) PreflightReviewChat(ctx context.Context, h domain.AgentHarness) error {
	return b.service.PreflightChat(ctx, h, ports.PermissionModeAuto)
}

func (b *probeBridge) prepareStart(ctx context.Context, cfg review.ReviewerChatStart, restore bool) (review.ReviewerChatStart, error) {
	if cfg.ReviewID == "" || cfg.WorkerID == "" || cfg.Harness != domain.HarnessCodex {
		return cfg, errors.New("invalid probe owner binding")
	}
	row, found, err := b.store.GetReviewByID(ctx, cfg.ReviewID)
	if err != nil || !found || row.IsArchived || row.SessionID != cfg.WorkerID || row.ProjectID != cfg.ProjectID {
		return cfg, errors.New("probe review owner binding mismatch")
	}
	if restore {
		cfg.Prompt = ""
		return cfg, nil
	}
	if cfg.ReviewerApproval == nil || cfg.ReviewerApproval.ReviewID != cfg.ReviewID || cfg.ReviewerApproval.WorkerID != cfg.WorkerID || cfg.ReviewerApproval.BatchID != cfg.BatchID {
		return cfg, errors.New("probe run binding missing")
	}
	if err := b.validateScope(ctx, *cfg.ReviewerApproval); err != nil {
		return cfg, err
	}
	copyScope := *cfg.ReviewerApproval
	b.scope, b.reviewID = &copyScope, cfg.ReviewID
	if cfg.Prompt != "" {
		cfg.Prompt = probeMessage(copyScope)
	}
	return cfg, nil
}

func (b *probeBridge) prepareNotify(ctx context.Context, reviewID, batchID string) (string, error) {
	if b.scope == nil || b.reviewID != reviewID || b.scope.BatchID != batchID {
		return "", errors.New("probe notification binding mismatch")
	}
	if err := b.validateScope(ctx, *b.scope); err != nil {
		return "", err
	}
	return probeMessage(*b.scope), nil
}

func (b *probeBridge) validateScope(ctx context.Context, scope ports.ReviewerApprovalScope) error {
	run, found, err := b.store.GetReviewRun(ctx, scope.RunID)
	if err != nil {
		return err
	}
	if !found || run.Status != domain.ReviewRunRunning {
		return errors.New("probe run is not running")
	}
	if scope.PRURL != fixtureURL || scope.TargetSHA != fixtureHead || *scopeForRun(run) != scope || run.Harness != domain.ReviewerCodex {
		return errors.New("probe target/run binding mismatch")
	}
	return nil
}

func (b *probeBridge) StartReviewChat(ctx context.Context, cfg review.ReviewerChatStart) (string, error) {
	prepared, err := b.prepareStart(ctx, cfg, false)
	if err != nil {
		return "", err
	}
	return b.start(ctx, prepared)
}

func (b *probeBridge) RestoreReviewChat(ctx context.Context, cfg review.ReviewerChatStart) (string, error) {
	prepared, err := b.prepareStart(ctx, cfg, true)
	if err != nil {
		return "", err
	}
	b.reviewID = cfg.ReviewID
	return b.start(ctx, prepared)
}

func (b *probeBridge) start(ctx context.Context, cfg review.ReviewerChatStart) (string, error) {
	owner := domain.ReviewConversationOwner(cfg.ReviewID)
	if b.host == nil {
		return "", errors.New("native probe host is not owned by this test")
	}
	if err := b.host.ensure(ctx, cfg); err != nil {
		return "", err
	}
	started, err := b.service.StartChat(ctx, chatsvc.StartConfig{
		Owner: owner, SessionID: cfg.WorkerID, ProjectID: cfg.ProjectID, Kind: domain.KindWorker,
		Harness: cfg.Harness, DataDir: cfg.DataDir, WorkspacePath: cfg.WorkspacePath, Env: cfg.Env,
		Model: cfg.Model, Effort: cfg.Effort, Permissions: ports.PermissionModeAuto,
		SystemPrompt: cfg.SystemPrompt, ProviderConversationID: cfg.ProviderConversationID, ReviewerApproval: cfg.ReviewerApproval,
	})
	if err != nil {
		return "", err
	}
	if cfg.Prompt != "" {
		_, err = b.service.SendForOwner(ctx, owner, ports.ChatUserMessage{Text: cfg.Prompt, Origin: domain.MessageOriginDaemon, ClientMessageID: review.BatchMessageID(cfg.BatchID)})
	}
	return started.ProviderConversationID, err
}

func (b *probeBridge) SendReviewChat(ctx context.Context, reviewID, _ string, batchID string) error {
	message, err := b.prepareNotify(ctx, reviewID, batchID)
	if err != nil {
		return err
	}
	_, err = b.service.SendForOwner(ctx, domain.ReviewConversationOwner(reviewID), ports.ChatUserMessage{Text: message, Origin: domain.MessageOriginDaemon, ClientMessageID: review.BatchMessageID(batchID)})
	return err
}

func (b *probeBridge) ReviewChatAlive(reviewID string) bool {
	c, err := b.service.ControllerForOwner(domain.ReviewConversationOwner(reviewID))
	return err == nil && c.State() != ports.ChatControllerStopped
}

func (b *probeBridge) InterruptReviewChat(ctx context.Context, reviewID string) error {
	return b.service.InterruptForOwner(ctx, domain.ReviewConversationOwner(reviewID))
}

func (b *probeBridge) StopReviewChat(ctx context.Context, reviewID string) error {
	return b.service.StopForOwner(ctx, domain.ReviewConversationOwner(reviewID))
}

func scopeForRun(run domain.ReviewRun) *ports.ReviewerApprovalScope {
	return &ports.ReviewerApprovalScope{ReviewID: run.ReviewID, RunID: run.ID, BatchID: run.BatchID, WorkerID: run.SessionID, PRURL: run.PRURL, TargetSHA: run.TargetSHA}
}

func probeCommand(runID string) string { return "printf '%s\\n' 'ao-review-probe:" + runID + "'" }

func probeMessage(scope ports.ReviewerApprovalScope) string {
	return fmt.Sprintf("This is an authorized approval-lifecycle probe for AO review %s, worker %s, run %s, batch %s, target %s, head %s. This is NOT a review task. Do not read the generated review-task file, inspect repositories, use MCP/provider/network tools, write files, submit a review receipt, or publish anything. Request native human approval for exactly one shell command, without running it first: %s . Request approve-once using the normal inherited approval protocol; do not alter policy/sandbox/configuration. If that exact request is unavailable or denied, report BLOCKED and stop; do not substitute another command. While pending, wait. If accepted, execute it exactly once, report its output, and stop. Do nothing else.", scope.ReviewID, scope.WorkerID, scope.RunID, scope.BatchID, scope.PRURL, scope.TargetSHA, probeCommand(scope.RunID))
}

func openNativeProbe(t *testing.T, dataDir, binary string, host *probeNativeHost) (*sqlite.Store, *chatsvc.Service, *probeBridge, *review.Engine) {
	t.Helper()
	st, err := sqlite.Open(dataDir)
	require.NoError(t, err)
	driver := codexappserver.New(worker.New(), slog.New(slog.DiscardHandler))
	svc := chatsvc.New(probeChatOptions(st, chatregistry.New(driver), dataDir))
	bridge := &probeBridge{store: st, service: svc, host: host}
	resolver, err := reviewregistry.NewResolver()
	require.NoError(t, err)
	launcher := review.NewLauncher(resolver, nil, dataDir, review.WithReviewerChat(bridge), review.WithExecutable(func() (string, error) { return binary, nil }), review.WithRunFilePath(filepath.Join(filepath.Dir(dataDir), "running.json")))
	engine := review.New(review.Deps{Store: st, Sessions: st, PRs: st, Projects: st, Launcher: launcher})
	return st, svc, bridge, engine
}

func probeChatOptions(st *sqlite.Store, drivers ports.ChatDriverRegistry, dataDir string) chatsvc.Options {
	reader := chatsvc.SnapshotReaderFunc(func(ctx context.Context, id string) (chatsvc.ConversationRows, error) {
		rows, err := st.LoadConversationSnapshot(ctx, id)
		if err != nil {
			return chatsvc.ConversationRows{}, err
		}
		return chatsvc.ConversationRows{
			Conversation: rows.Conversation, ActiveBranch: rows.ActiveBranch,
			EditFloorSequence: rows.EditFloorSequence, NativeForkAvailableAfterSequence: rows.NativeForkAvailableAfterSequence,
			Turns: rows.Turns, Messages: rows.Messages, Activities: rows.Activities, BranchPoints: rows.BranchPoints,
			BranchedFromEarlierMessage: rows.BranchedFromEarlierMessage,
			OldestSequence:             rows.OldestSequence, HasMoreBefore: rows.HasMoreBefore,
		}, nil
	})
	return chatsvc.Options{Store: st, Reader: reader, Sessions: st, Drivers: drivers, DataDir: dataDir, NewID: uuid.NewString}
}

// The production host entrypoint owns only its Codex child process group. It
// stays alive while Chat/store attachments are reconstructed. Running it here
// avoids asking ConnectOrStart to launch the test binary as an AO CLI host.
type probeNativeHost struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	err    error // Read only after done closes.
	config persistenthost.Config
}

func (h *probeNativeHost) ensure(ctx context.Context, start review.ReviewerChatStart) error {
	if h.done == nil {
		binary, err := worker.New().ResolveBinary(ctx)
		if err != nil {
			return err
		}
		h.config = probeHostConfig(start, binary, processenv.Merge(start.Env))
		hostCtx, cancel := context.WithCancel(h.ctx)
		h.cancel = cancel
		h.done = make(chan struct{})
		go func() {
			h.err = persistenthost.Run(hostCtx, h.config)
			close(h.done)
		}()
	}
	if h.config.SessionID != "review-"+start.ReviewID || h.config.DataDir != start.DataDir || h.config.Workdir != start.WorkspacePath {
		return errors.New("native probe host ownership changed")
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return fmt.Errorf("native probe host stopped before attachment: %v", h.err)
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if pid, ok := persistenthost.HostPID(h.config.DataDir, h.config.SessionID); ok {
			if pid != os.Getpid() {
				return errors.New("native probe host belongs to another process")
			}
			return nil
		}
		select {
		case <-h.done:
			return fmt.Errorf("native probe host startup failed: %v", h.err)
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func probeHostConfig(start review.ReviewerChatStart, binary string, env []string) persistenthost.Config {
	return persistenthost.Config{SessionID: "review-" + start.ReviewID, DataDir: start.DataDir, Workdir: start.WorkspacePath, Env: env, Argv: []string{binary, "app-server"}, Protocol: persistenthost.ProtocolRaw}
}

func (h *probeNativeHost) stop(ctx context.Context) error {
	if h.done == nil {
		return nil
	}
	h.cancel()
	select {
	case <-h.done:
		if errors.Is(h.err, context.Canceled) {
			return nil
		}
		return h.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func validateLabPaths(root, binary string) error {
	if !filepath.IsAbs(root) || !filepath.IsAbs(binary) || filepath.Clean(root) != root || filepath.Clean(binary) != binary {
		return errors.New("absolute clean lab and binary paths are required")
	}
	parent := filepath.Dir(root)
	if filepath.Base(parent) != "labs" || filepath.Base(filepath.Dir(parent)) != ".ao" {
		return errors.New("lab root must be directly inside the user's .ao/labs")
	}
	relative, err := filepath.Rel(root, binary)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("AO binary must belong to this lab")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil || resolvedRoot != root {
		return errors.New("lab root must exist without symlink redirection")
	}
	resolvedBinary, err := filepath.EvalSymlinks(binary)
	if err != nil || resolvedBinary != binary {
		return errors.New("lab binary must exist without symlink redirection")
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("lab binary must be a regular file")
	}
	return nil
}

func waitProbePending(t *testing.T, ctx context.Context, svc *chatsvc.Service, scope *ports.ReviewerApprovalScope, workspace string) (chatsvc.Snapshot, domain.ConversationActivity) {
	t.Helper()
	for {
		snapshot, err := svc.SnapshotForReview(ctx, scope.ReviewID)
		require.NoError(t, err)
		for _, activity := range snapshot.Activities {
			if activity.Kind == domain.ActivityKindMCPTool || activity.Kind == domain.ActivityKindFileChange || activity.Kind == domain.ActivityKindAOAction {
				t.Fatal("native probe attempted an operation outside its single harmless command")
			}
			if activity.Status == domain.ActivityStatusPending {
				require.NoError(t, validateProbeApproval(snapshot, activity, *scope, workspace))
				return snapshot, activity
			}
		}
		for _, turn := range snapshot.Turns {
			if turn.CompletedAt != nil && turn.State != domain.TurnStateInterrupted {
				t.Fatalf("native turn settled without the required real pending approval: %s", turn.State)
			}
		}
		waitProbeTick(t, ctx)
	}
}

func waitProbeTerminal(t *testing.T, ctx context.Context, svc *chatsvc.Service, reviewID, turnID string, state domain.TurnState) chatsvc.Snapshot {
	t.Helper()
	for {
		snapshot, err := svc.SnapshotForReview(ctx, reviewID)
		require.NoError(t, err)
		for _, activity := range snapshot.Activities {
			if activity.Kind == domain.ActivityKindMCPTool || activity.Kind == domain.ActivityKindFileChange || activity.Kind == domain.ActivityKindAOAction {
				t.Fatal("native probe attempted an operation outside its single harmless command")
			}
		}
		for _, turn := range snapshot.Turns {
			if turn.ID == turnID && turn.CompletedAt != nil {
				require.Equal(t, state, turn.State)
				return snapshot
			}
		}
		waitProbeTick(t, ctx)
	}
}

func waitProbeTick(t *testing.T, ctx context.Context) {
	t.Helper()
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
}

func validateProbeApproval(snapshot chatsvc.Snapshot, activity domain.ConversationActivity, scope ports.ReviewerApprovalScope, workspace string) error {
	var detail struct {
		Method    string                      `json:"method"`
		Command   string                      `json:"command"`
		Cwd       string                      `json:"cwd"`
		Scope     ports.ReviewerApprovalScope `json:"reviewerScope"`
		Decisions []struct {
			ID string `json:"id"`
		} `json:"decisions"`
		AdditionalPermissions json.RawMessage `json:"additionalPermissions"`
		Network               json.RawMessage `json:"networkApprovalContext"`
	}
	if err := json.Unmarshal(activity.Detail, &detail); err != nil {
		return err
	}
	if activity.Kind != domain.ActivityKindApproval || activity.Status != domain.ActivityStatusPending || detail.Method != "item/commandExecution/requestApproval" || detail.Command != probeCommand(scope.RunID) || detail.Cwd != workspace || detail.Scope != scope || len(detail.AdditionalPermissions) != 0 || len(detail.Network) != 0 || !strings.Contains(activity.RequestID, ":"+scope.RunID+":") {
		return errors.New("actual native pending request does not match the harmless once-only scope")
	}
	accept := false
	for _, decision := range detail.Decisions {
		switch decision.ID {
		case "accept":
			accept = true
		case "decline", "cancel":
		default:
			return errors.New("native request includes a persistent or unsupported decision")
		}
	}
	if !accept || snapshot.ActiveBranch.ProviderConversationID == "" {
		return errors.New("native request lacks its thread or approve-once decision")
	}
	for _, turn := range snapshot.Turns {
		if turn.ID == activity.TurnID && turn.HandledByReviewID == scope.ReviewID && turn.ProviderTurnID != "" && turn.CompletedAt == nil {
			return nil
		}
	}
	return errors.New("native request is not bound to the current live reviewer turn")
}

func requestPending(snapshot chatsvc.Snapshot, requestID string) bool {
	for _, activity := range snapshot.Activities {
		if activity.RequestID == requestID && activity.Status == domain.ActivityStatusPending {
			return true
		}
	}
	return false
}

func requireNoPending(snapshot chatsvc.Snapshot) error {
	for _, activity := range snapshot.Activities {
		if activity.Status == domain.ActivityStatusPending {
			return errors.New("native interaction remains pending")
		}
	}
	return nil
}

func completedProbeCommands(snapshot chatsvc.Snapshot, runID string) int {
	count := 0
	for _, activity := range snapshot.Activities {
		if activity.Kind != domain.ActivityKindCommand || activity.Status != domain.ActivityStatusCompleted {
			continue
		}
		var detail struct {
			Command  string `json:"command"`
			ExitCode *int   `json:"exitCode"`
		}
		if json.Unmarshal(activity.Detail, &detail) == nil && detail.Command == probeCommand(runID) && detail.ExitCode != nil && *detail.ExitCode == 0 && strings.TrimSpace(activity.CommandOutput) == "ao-review-probe:"+runID {
			count++
		}
	}
	return count
}

func probeExecutionCount(snapshot chatsvc.Snapshot, runID string) int {
	count := 0
	for _, activity := range snapshot.Activities {
		if activity.Kind != domain.ActivityKindCommand {
			continue
		}
		var detail struct {
			Command  string `json:"command"`
			ExitCode *int   `json:"exitCode"`
		}
		if json.Unmarshal(activity.Detail, &detail) == nil && detail.Command == probeCommand(runID) && (detail.ExitCode != nil || activity.CommandOutput != "") {
			count++
		}
	}
	return count
}

func logProbePending(t *testing.T, phase, server string, snapshot chatsvc.Snapshot, request domain.ConversationActivity, scope *ports.ReviewerApprovalScope) {
	t.Helper()
	var detail struct {
		Cwd string `json:"cwd"`
	}
	require.NoError(t, json.Unmarshal(request.Detail, &detail))
	for _, turn := range snapshot.Turns {
		if turn.ID == request.TurnID {
			t.Logf("NATIVE_PENDING phase=%s url=%s review=%s worker=%s run=%s thread=%s turn=%s request=%s command=%q cwd=%q", phase, server, scope.ReviewID, scope.WorkerID, scope.RunID, snapshot.ActiveBranch.ProviderConversationID, turn.ProviderTurnID, request.RequestID, probeCommand(scope.RunID), detail.Cwd)
		}
	}
}

func probeApprovalHandler(next http.Handler, svc *chatsvc.Service, scope *ports.ReviewerApprovalScope, requestID, workspace string) http.Handler {
	base := "/api/v1/reviews/" + scope.ReviewID + "/conversation"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == base {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != base+"/approvals/"+requestID+"/resolve" {
			http.NotFound(w, r)
			return
		}
		var decision struct {
			DecisionID string `json:"decisionId"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&decision) != nil || decoder.Decode(&struct{}{}) != io.EOF || decision.DecisionID != "accept" {
			http.Error(w, "only the exact human approve-once decision is supported", http.StatusBadRequest)
			return
		}
		snapshot, err := svc.SnapshotForReview(r.Context(), scope.ReviewID)
		if err != nil {
			http.Error(w, "review snapshot unavailable", http.StatusConflict)
			return
		}
		for _, activity := range snapshot.Activities {
			if activity.RequestID == requestID && validateProbeApproval(snapshot, activity, *scope, workspace) == nil {
				r.Body = io.NopCloser(strings.NewReader(`{"decisionId":"accept"}`))
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, "exact current native request is not pending", http.StatusConflict)
	})
}
