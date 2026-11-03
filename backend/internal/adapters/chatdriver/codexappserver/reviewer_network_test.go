package codexappserver

import (
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestReviewerResolvedNetworkCapabilityMatchesTheNativeTurn(t *testing.T) {
	for _, network := range []string{"false", "true"} {
		t.Run(network, func(t *testing.T) {
			d, srv := newTestDriver(t)
			sandbox := `{"type":"readOnly","networkAccess":` + network + `}`
			srv.respondTo("thread/start", `{"thread":{"id":"thread-1"},"approvalPolicy":"on-request","approvalsReviewer":"user","sandbox":`+sandbox+`}`)
			conv, err := d.Start(t.Context(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws", ReadOnly: true, ReviewerApproval: reviewerApprovalFixture()})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, conv.Close()) })
			// The render service consumes precisely this port, independently of the
			// next-turn approval-mode setting. A native deny must remain a deny.
			networkPort, ok := conv.(ports.ChatSandboxNetwork)
			require.True(t, ok)
			require.Equal(t, network == "true", networkPort.SandboxAllowsNetwork(ports.PermissionModeBypassPermissions))
			_, err = conv.SendTurn(t.Context(), ports.ChatUserMessage{Text: "Review", Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeBypassPermissions}})
			require.NoError(t, err)
			turn := srv.awaitFrame(func(f frame) bool { return f.Method == "turn/start" })
			var params map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(turn.Params, &params))
			require.JSONEq(t, sandbox, string(params["sandboxPolicy"]))
		})
	}
}

func TestReviewerUnresolvedNetworkFailsClosedAndDefaultsStayUnchanged(t *testing.T) {
	for _, raw := range []string{"", `null`, `{`, `{"type":"readOnly"}`, `{"type":"readOnly","networkAccess":"yes"}`, `{"type":"workspaceWrite","networkAccess":true}`} {
		t.Run(raw, func(t *testing.T) {
			conv := &conversation{readOnly: true, reviewerSandbox: json.RawMessage(raw)}
			conv.reviewerApproval.Store(reviewerApprovalFixture())
			require.False(t, conv.SandboxAllowsNetwork(ports.PermissionModeBypassPermissions))
			params := map[string]any{"sandboxPolicy": map[string]any{"type": "readOnly", "networkAccess": true}}
			conv.applyReviewerApproval(params)
			encoded, err := json.Marshal(params["sandboxPolicy"])
			require.NoError(t, err)
			_, network := reviewerSandboxNetwork(encoded)
			require.False(t, network, "unknown native sandbox must not widen turn network access")
		})
	}
	require.True(t, (&conversation{readOnly: true}).SandboxAllowsNetwork(ports.PermissionModeAuto), "ordinary/GitHub read-only reviewer default is unchanged")
	require.False(t, (&conversation{readOnly: true, disableNetwork: true}).SandboxAllowsNetwork(ports.PermissionModeAuto), "background deny is unchanged")
	require.False(t, (&conversation{launchMode: ports.PermissionModeAuto}).SandboxAllowsNetwork(""), "ordinary auto worker sandbox is unchanged")
}
