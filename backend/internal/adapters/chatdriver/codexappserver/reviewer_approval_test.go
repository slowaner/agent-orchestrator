package codexappserver

import (
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestReviewerApprovalUsesInheritedPolicyOnStartResumeAndTurn(t *testing.T) {
	for _, policy := range []string{`"on-request"`, `"never"`, `{"granular":{"mcp_elicitations":false,"rules":false,"sandbox_approval":false}}`} {
		for _, resume := range []bool{false, true} {
			t.Run(policy+map[bool]string{false: "/start", true: "/resume"}[resume], func(t *testing.T) {
				d, srv := newTestDriver(t)
				srv.respondTo("config/read", `{"config":{"approval_policy":`+policy+`}}`)
				srv.respondTo("thread/start", `{"thread":{"id":"thread-1"},"approvalsReviewer":"user","sandbox":{"type":"readOnly","networkAccess":true},"approvalPolicy":`+policy+`}`)
				srv.respondTo("thread/resume", `{"thread":{"id":"thread-1"},"approvalsReviewer":"user","sandbox":{"type":"readOnly","networkAccess":true},"approvalPolicy":`+policy+`}`)
				var conv ports.ChatConversation
				var err error
				method := "thread/start"
				if resume {
					method = "thread/resume"
					conv, err = d.Resume(t.Context(), ports.ChatResumeConfig{WorkspacePath: "/tmp/ws", ReadOnly: true, ReviewerApproval: reviewerApprovalFixture(), ProviderConversationID: "thread-1"})
				} else {
					conv, err = d.Start(t.Context(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", ReadOnly: true, ReviewerApproval: reviewerApprovalFixture()})
				}
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, conv.Close()) })
				config := srv.awaitFrame(func(f frame) bool { return f.Method == "config/read" })
				var configParams map[string]any
				require.NoError(t, json.Unmarshal(config.Params, &configParams))
				require.Equal(t, "/tmp/ws", configParams["cwd"])
				opened := srv.awaitFrame(func(f frame) bool { return f.Method == method })
				var params map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(opened.Params, &params))
				require.JSONEq(t, policy, string(params["approvalPolicy"]))
				require.JSONEq(t, `"read-only"`, string(params["sandbox"]))
				require.JSONEq(t, `"user"`, string(params["approvalsReviewer"]))
				_, err = conv.SendTurn(t.Context(), ports.ChatUserMessage{Text: "Review", Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeBypassPermissions}})
				require.NoError(t, err)
				turn := srv.awaitFrame(func(f frame) bool { return f.Method == "turn/start" })
				require.NoError(t, json.Unmarshal(turn.Params, &params))
				require.JSONEq(t, policy, string(params["approvalPolicy"]))
				require.JSONEq(t, `{"type":"readOnly","networkAccess":true}`, string(params["sandboxPolicy"]))
				require.JSONEq(t, `"user"`, string(params["approvalsReviewer"]))
			})
		}
	}
}

func TestReviewerApprovalMissingRawPolicyDefersToNativeStartResumeAndTurn(t *testing.T) {
	for _, policy := range []string{`"on-request"`, `"never"`} {
		for _, resume := range []bool{false, true} {
			t.Run(policy+map[bool]string{false: "/start", true: "/resume"}[resume], func(t *testing.T) {
				d, srv := newTestDriver(t)
				method := "thread/start"
				if resume {
					method = "thread/resume"
				}
				srv.respondTo(method, `{"thread":{"id":"thread-1"},"approvalsReviewer":"user","sandbox":{"type":"readOnly","networkAccess":true},"approvalPolicy":`+policy+`}`)
				var conv ports.ChatConversation
				var err error
				if resume {
					conv, err = d.Resume(t.Context(), ports.ChatResumeConfig{WorkspacePath: "/tmp/ws", ProviderConversationID: "thread-1", ReadOnly: true, ReviewerApproval: reviewerApprovalFixture()})
				} else {
					conv, err = d.Start(t.Context(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", ReadOnly: true, ReviewerApproval: reviewerApprovalFixture()})
				}
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, conv.Close()) })
				opened := srv.awaitFrame(func(f frame) bool { return f.Method == method })
				var params map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(opened.Params, &params))
				require.NotContains(t, params, "approvalPolicy")
				require.JSONEq(t, `"read-only"`, string(params["sandbox"]))
				_, err = conv.SendTurn(t.Context(), ports.ChatUserMessage{Text: "Review", Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAuto}})
				require.NoError(t, err)
				turn := srv.awaitFrame(func(f frame) bool { return f.Method == "turn/start" })
				params = nil
				require.NoError(t, json.Unmarshal(turn.Params, &params))
				require.NotContains(t, params, "approvalPolicy")
				require.JSONEq(t, `"user"`, string(params["approvalsReviewer"]))
			})
		}
	}
}

func TestReviewerApprovalMissingResolvedPolicyFailsClosed(t *testing.T) {
	d, _ := newTestDriver(t)
	_, err := d.Start(t.Context(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", ReadOnly: true, ReviewerApproval: reviewerApprovalFixture()})
	require.ErrorContains(t, err, "supported resolved")
}

func TestReviewerApprovalRejectsMalformedAndContradictoryPolicies(t *testing.T) {
	for _, test := range []struct{ name, configured, resolved, message string }{
		{"malformed", `false`, `"on-request"`, "invalid inherited"},
		{"unknown", `"unknown"`, `"on-request"`, "invalid inherited"},
		{"incomplete-granular", `{"granular":{"mcp_elicitations":false}}`, `"on-request"`, "invalid inherited"},
		{"explicit-never", `"never"`, `"on-request"`, "contradicts inherited"},
		{"granular-deny", `{"granular":{"mcp_elicitations":false,"rules":false,"sandbox_approval":false}}`, `"on-request"`, "contradicts inherited"},
		{"unknown-resolved", `null`, `"unknown"`, "supported resolved"},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, srv := newTestDriver(t)
			srv.respondTo("config/read", `{"config":{"approval_policy":`+test.configured+`}}`)
			srv.respondTo("thread/start", `{"thread":{"id":"thread-1"},"approvalsReviewer":"user","sandbox":{"type":"readOnly","networkAccess":true},"approvalPolicy":`+test.resolved+`}`)
			_, err := d.Start(t.Context(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", ReadOnly: true, ReviewerApproval: reviewerApprovalFixture()})
			require.ErrorContains(t, err, test.message)
			require.False(t, srv.sentMethod("turn/start"))
		})
	}
}

func TestReviewerMCPApprovalRequiresHumanOnceAndRejectsReplay(t *testing.T) {
	for _, decision := range []string{"accept", "decline", "cancel"} {
		t.Run(decision, func(t *testing.T) {
			conv, srv := startReviewerApprovalFixture(t)
			srv.push(`{"id":"mcp-1","method":"mcpServer/elicitation/request","params":{"threadId":"thread-1","turnId":"turn-1","serverName":"gitlab","mode":"form","message":"Create a fixture draft?","_meta":{"codex_approval_kind":"mcp_tool_call","persist":["session","always"],"tool_params":{"project_id":"group/repo","merge_request_iid":1}},"requestedSchema":{"type":"object","properties":{}}}}`)
			ev := nextEvent(t, conv.Events(), ports.ChatEventApprovalRequested)
			require.Contains(t, ev.RequestID, ":run-1:mcp-1")
			require.Len(t, ev.Decisions, 3)
			require.Contains(t, string(ev.Detail), "reviewerScope")
			require.ErrorIs(t, conv.ResolveRequest(t.Context(), ev.RequestID, ports.ChatDecision{ID: "acceptForSession"}), ports.ErrChatDecisionNotOffered)
			require.NoError(t, conv.ResolveRequest(t.Context(), ev.RequestID, ports.ChatDecision{ID: decision, Raw: []byte(`{"persist":"always"}`)}))
			reply := srv.awaitFrame(func(f frame) bool { return f.ID != nil && string(*f.ID) == `"mcp-1"` && f.Method == "" })
			require.JSONEq(t, `{"action":"`+decision+`","content":null}`, string(reply.Result))
			require.ErrorIs(t, conv.ResolveRequest(t.Context(), ev.RequestID, ports.ChatDecision{ID: "accept"}), ports.ErrChatRequestNotPending)
		})
	}
}

func TestReviewerLegacyMCPApprovalCannotRememberOrUseInputEndpoint(t *testing.T) {
	conv, srv := startReviewerApprovalFixture(t)
	srv.push(`{"id":"legacy-1","method":"item/tool/requestUserInput","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"tool-1","questions":[{"id":"mcp_tool_call_approval_tool-1","header":"Approve app tool call?","question":"Create a fixture draft?","isOther":false,"isSecret":false,"options":[{"label":"Allow","description":"Run once"},{"label":"Allow for this session","description":"Remember"},{"label":"Allow and don't ask me again","description":"Always"},{"label":"Cancel","description":"Stop"}]}]}}`)
	ev := nextEvent(t, conv.Events(), ports.ChatEventApprovalRequested)
	require.Len(t, ev.Decisions, 2)
	require.ErrorIs(t, conv.ResolveInput(t.Context(), ev.RequestID, ports.ChatInputResponse{Action: ports.ChatInputActionAccept}), ports.ErrChatRequestNotPending)
	require.NoError(t, conv.ResolveRequest(t.Context(), ev.RequestID, ports.ChatDecision{ID: "accept"}))
	reply := srv.awaitFrame(func(f frame) bool { return f.ID != nil && string(*f.ID) == `"legacy-1"` && f.Method == "" })
	require.JSONEq(t, `{"answers":{"mcp_tool_call_approval_tool-1":{"answers":["Allow"]}}}`, string(reply.Result))
}

func TestReviewerApprovalRejectsCrossThreadUnknownFormAndCompletedTurn(t *testing.T) {
	for _, params := range []string{
		`{"threadId":"other-thread","turnId":"turn-1","serverName":"gitlab","mode":"form","message":"Approve?","_meta":{"codex_approval_kind":"mcp_tool_call"},"requestedSchema":{"type":"object","properties":{}}}`,
		`{"threadId":"thread-1","turnId":"turn-1","serverName":"gitlab","mode":"form","message":"Approve?","_meta":{"codex_approval_kind":"browser_auth"},"requestedSchema":{"type":"object","properties":{}}}`,
		`{"threadId":"thread-1","turnId":"turn-1","serverName":"gitlab","mode":"form","message":"Approve?","_meta":{"codex_approval_kind":"mcp_tool_call"},"requestedSchema":{"type":"object","properties":{"token":{"type":"string"}}}}`,
	} {
		t.Run(params, func(t *testing.T) {
			conv, srv := startReviewerApprovalFixture(t)
			srv.push(`{"id":"bad-1","method":"mcpServer/elicitation/request","params":` + params + `}`)
			reply := srv.awaitFrame(func(f frame) bool { return f.ID != nil && string(*f.ID) == `"bad-1"` && f.Method == "" })
			require.NotNil(t, reply.Error)
			require.ErrorIs(t, conv.ResolveRequest(t.Context(), "bad-1", ports.ChatDecision{ID: "accept"}), ports.ErrChatRequestNotPending)
		})
	}
	conv, srv := startReviewerApprovalFixture(t)
	srv.push(`{"id":"command-1","method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"exec-1","command":"glab api --hostname gitlab.example.test projects/group%2Frepo/merge_requests/1","availableDecisions":["accept","acceptForSession","decline","cancel"]}}`)
	ev := nextEvent(t, conv.Events(), ports.ChatEventApprovalRequested)
	require.Len(t, ev.Decisions, 3)
	srv.push(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[]}}}`)
	nextEvent(t, conv.Events(), ports.ChatEventTurnCompleted)
	require.ErrorIs(t, conv.ResolveRequest(t.Context(), ev.RequestID, ports.ChatDecision{ID: "accept"}), ports.ErrChatRequestNotPending)
}

func TestReviewerApprovalRebindRequiresIdleAndRestartDropsOldPending(t *testing.T) {
	conv, srv := startReviewerApprovalFixture(t)
	next := reviewerApprovalFixture()
	next.RunID, next.BatchID = "run-2", "batch-2"
	require.ErrorContains(t, conv.SetReviewerApprovalScope(t.Context(), next), "active work")
	srv.push(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[]}}}`)
	nextEvent(t, conv.Events(), ports.ChatEventTurnCompleted)
	require.NoError(t, conv.SetReviewerApprovalScope(t.Context(), next))
	_, err := conv.SendTurn(t.Context(), ports.ChatUserMessage{Text: "Next review pass"})
	require.NoError(t, err)
	srv.push(`{"id":"pending-old","method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"exec-2","command":"glab api --hostname gitlab.example.test projects/group%2Frepo/merge_requests/1","availableDecisions":["accept","cancel"]}}`)
	ev := nextEvent(t, conv.Events(), ports.ChatEventApprovalRequested)
	require.Contains(t, string(ev.Detail), "run-2")
	require.NoError(t, conv.Close())
	require.Error(t, conv.ResolveRequest(t.Context(), ev.RequestID, ports.ChatDecision{ID: "accept"}))
	d, restarted := newTestDriver(t)
	restarted.respondTo("config/read", `{"config":{"approval_policy":"on-request"}}`)
	restarted.respondTo("thread/resume", `{"thread":{"id":"thread-1"},"approvalsReviewer":"user","sandbox":{"type":"readOnly","networkAccess":true},"approvalPolicy":"on-request"}`)
	resumed, err := d.Resume(t.Context(), ports.ChatResumeConfig{WorkspacePath: "/tmp/ws", ProviderConversationID: "thread-1", ReadOnly: true, ReviewerApproval: next})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resumed.Close()) })
	_, err = resumed.SendTurn(t.Context(), ports.ChatUserMessage{Text: "Resume fixture"})
	require.NoError(t, err)
	restarted.push(`{"id":"pending-old","method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"exec-2","command":"glab api --hostname gitlab.example.test projects/group%2Frepo/merge_requests/1","availableDecisions":["accept","cancel"]}}`)
	fresh := nextEvent(t, resumed.Events(), ports.ChatEventApprovalRequested)
	require.NotEqual(t, ev.RequestID, fresh.RequestID)
	require.NotEqual(t, ev.ProviderItemID, fresh.ProviderItemID)
	require.ErrorIs(t, resumed.ResolveRequest(t.Context(), ev.RequestID, ports.ChatDecision{ID: "accept"}), ports.ErrChatRequestNotPending)
	require.NoError(t, resumed.ResolveRequest(t.Context(), fresh.RequestID, ports.ChatDecision{ID: "cancel"}))
}

func reviewerApprovalFixture() *ports.ReviewerApprovalScope {
	return &ports.ReviewerApprovalScope{ReviewID: "review-1", RunID: "run-1", BatchID: "batch-1", WorkerID: domain.SessionID("worker-1"), PRURL: "https://gitlab.example.test/group/repo/-/merge_requests/1", TargetSHA: "0123456789012345678901234567890123456789"}
}

func startReviewerApprovalFixture(t *testing.T) (*conversation, *scriptedServer) {
	t.Helper()
	d, srv := newTestDriver(t)
	srv.respondTo("config/read", `{"config":{"approval_policy":"on-request"}}`)
	srv.respondTo("thread/start", `{"thread":{"id":"thread-1"},"approvalsReviewer":"user","sandbox":{"type":"readOnly","networkAccess":true},"approvalPolicy":"on-request"}`)
	conv, err := d.Start(t.Context(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", ReadOnly: true, ReviewerApproval: reviewerApprovalFixture()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conv.Close()) })
	_, err = conv.SendTurn(t.Context(), ports.ChatUserMessage{Text: "Review fixture"})
	require.NoError(t, err)
	return conv.(*conversation), srv
}
