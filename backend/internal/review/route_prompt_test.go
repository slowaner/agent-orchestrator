package review

import (
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/stretchr/testify/require"
)

// This checks generated instructions, not provider execution or permission grants.
func TestPublicationRouteInstructionsStayPerTask(t *testing.T) {
	queue := reviewQueue([]domain.ReviewRun{
		{ID: "glab-run", PRURL: "https://gitlab.example/g/a/-/merge_requests/1", PublicationRoute: domain.ReviewPublicationGlab},
		{ID: "mcp-run", PRURL: "https://gitlab.example/g/b/-/merge_requests/2", PublicationRoute: domain.ReviewPublicationMCP},
		{ID: "gh-run", PRURL: "https://github.com/o/r/pull/3"},
	})
	for _, tasks := range [][]ports.ReviewTask{queue, {queue[2], queue[1], queue[0]}} {
		prompt, _ := reviewTexts(LaunchSpec{ReviewQueue: tasks, PRURL: tasks[0].PRURL})
		for _, tc := range []struct{ id, provider, want, absent string }{
			{"glab-run", "GitLab", "glab ONLY", "gitlab_mr_draft_note_create"},
			{"mcp-run", "GitLab", "MCP ONLY", "glab api"},
			{"gh-run", "GitHub", "gh api --method POST", "Selected publication route"},
		} {
			_, task, ok := strings.Cut(prompt, "BEGIN "+tc.provider+" task "+tc.id+"\n")
			require.True(t, ok)
			task, _, ok = strings.Cut(task, "\nEND "+tc.provider+" task "+tc.id)
			require.True(t, ok)
			require.Contains(t, task, tc.want)
			require.NotContains(t, task, tc.absent)
		}
	}
}
