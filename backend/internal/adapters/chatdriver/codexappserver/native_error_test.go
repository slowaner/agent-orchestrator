package codexappserver

import (
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver/codexproto"
	"github.com/stretchr/testify/require"
)

func TestNativeMCPErrorRetainsSerializableCauseAndConservativeOutcome(t *testing.T) {
	for _, test := range []struct{ message, outcome string }{
		{"MCP tool call requires approval, but approval policy is never", "pre_execution_denial"},
		{"user rejected MCP tool call", "pre_execution_denial"},
		{"request timed out", "unknown"},
		{"HTTP 503", "unknown"},
		{"missing draft ID", "unknown"},
		{"Unable to store draft result", "unknown"},
	} {
		t.Run(test.message, func(t *testing.T) {
			detail := activityDetail(threadItem{Type: itemMcpToolCall, Error: &codexproto.McpToolCallError{Message: test.message}})
			var plain map[string]any
			require.NoError(t, json.Unmarshal(detail, &plain))
			require.Equal(t, test.message, plain["error"])
			require.Equal(t, test.outcome, plain["errorOutcome"])
			encoded, err := json.Marshal(plain)
			require.NoError(t, err)
			require.JSONEq(t, string(detail), string(encoded))
		})
	}
}
