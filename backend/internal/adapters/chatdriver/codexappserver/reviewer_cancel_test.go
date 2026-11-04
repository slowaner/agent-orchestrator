package codexappserver

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestReviewerRestoreWithoutScopeRefusesNativeAndReservedMCPApprovals(t *testing.T) {
	d, srv := newTestDriver(t)
	conv, err := d.Resume(t.Context(), ports.ChatResumeConfig{WorkspacePath: "/tmp/ws", ProviderConversationID: "thread-1", ReadOnly: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conv.Close()) })
	for _, request := range []string{
		`{"id":"old-command","method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"cancelled-turn","command":"git status --short --branch","availableDecisions":["accept","acceptForSession","cancel"]}}`,
		`{"id":"old-mcp","method":"item/tool/requestUserInput","params":{"threadId":"thread-1","turnId":"cancelled-turn","itemId":"tool-old","questions":[{"id":"mcp_tool_call_approval_old","header":"Approve?","question":"Allow tool?","options":[{"label":"Allow","description":"Once"},{"label":"Allow for this session","description":"Session"},{"label":"Cancel","description":"Cancel"}]}]}}`,
	} {
		srv.push(request)
		reply := srv.awaitFrame(func(f frame) bool { return f.ID != nil && f.Method == "" })
		require.NotNil(t, reply.Error)
	}
	require.ErrorIs(t, conv.ResolveRequest(t.Context(), "old-command", ports.ChatDecision{ID: "accept"}), ports.ErrChatRequestNotPending)
	require.ErrorIs(t, conv.(ports.ChatInputResponder).ResolveInput(t.Context(), "old-mcp", ports.ChatInputResponse{Action: ports.ChatInputActionAccept, Content: map[string]any{"mcp_tool_call_approval_old": "Allow"}}), ports.ErrChatRequestNotPending)
}

func TestReviewerConfirmedInterruptionCancelsMatchingPendingAndAllowsRebind(t *testing.T) {
	conv, srv := startReviewerApprovalFixture(t)
	srv.push(`{"id":"old","method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"exec-old","command":"git status --short --branch"}}`)
	pending := nextEvent(t, conv.Events(), ports.ChatEventApprovalRequested)
	require.Len(t, pending.Decisions, 3, "known native missing-choice variant offers only one-shot decisions")
	require.ErrorIs(t, conv.ResolveRequest(t.Context(), pending.RequestID, ports.ChatDecision{ID: "acceptForSession"}), ports.ErrChatDecisionNotOffered)
	next := reviewerApprovalFixture()
	next.RunID = "run-after-cancel"
	require.ErrorContains(t, conv.SetReviewerApprovalScope(t.Context(), next), "active work")
	srv.push(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"interrupted","items":[]}}}`)
	resolved := nextEvent(t, conv.Events(), ports.ChatEventApprovalResolved)
	require.Equal(t, pending.RequestID, resolved.RequestID)
	nextEvent(t, conv.Events(), ports.ChatEventTurnCompleted)
	reply := srv.awaitFrame(func(f frame) bool { return f.ID != nil && string(*f.ID) == `"old"` && f.Method == "" })
	require.JSONEq(t, `{"decision":"cancel"}`, string(reply.Result))
	require.ErrorIs(t, conv.ResolveRequest(t.Context(), pending.RequestID, ports.ChatDecision{ID: "accept"}), ports.ErrChatRequestNotPending)
	require.NoError(t, conv.SetReviewerApprovalScope(t.Context(), next))
}

func TestReviewerOtherCompletedTurnPreservesCurrentHumanPendingRequest(t *testing.T) {
	conv, srv := startReviewerApprovalFixture(t)
	srv.push(`{"id":"current","method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"exec-current","command":"git status --short --branch"}}`)
	pending := nextEvent(t, conv.Events(), ports.ChatEventApprovalRequested)
	srv.push(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"older-turn","status":"completed","items":[]}}}`)
	nextEvent(t, conv.Events(), ports.ChatEventTurnCompleted)
	require.NoError(t, conv.ResolveRequest(t.Context(), pending.RequestID, ports.ChatDecision{ID: "accept"}))
	reply := srv.awaitFrame(func(f frame) bool { return f.ID != nil && string(*f.ID) == `"current"` && f.Method == "" })
	require.JSONEq(t, `{"decision":"accept"}`, string(reply.Result))
}

func TestReadOnlyGitHubDefaultRetainsOrdinaryUserQuestions(t *testing.T) {
	d, srv := newTestDriver(t)
	conv, err := d.Start(t.Context(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", ReadOnly: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conv.Close()) })
	srv.push(`{"id":"question","method":"item/tool/requestUserInput","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"question-item","questions":[{"id":"explanation","header":"Context","question":"Which behavior is intended?","options":[{"label":"Preserve","description":"Keep current behavior"}]}]}}`)
	pending := nextEvent(t, conv.Events(), ports.ChatEventInputRequested)
	require.NoError(t, conv.(ports.ChatInputResponder).ResolveInput(t.Context(), pending.RequestID, ports.ChatInputResponse{Action: ports.ChatInputActionAccept, Content: map[string]any{"explanation": "Preserve"}}))
	reply := srv.awaitFrame(func(f frame) bool { return f.ID != nil && string(*f.ID) == `"question"` && f.Method == "" })
	require.JSONEq(t, `{"answers":{"explanation":{"answers":["Preserve"]}}}`, string(reply.Result))
}
