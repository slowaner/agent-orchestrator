package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// The bounded MCP approval encodings below match codex-cli 0.164.0:
// codex-rs/core/src/mcp_tool_call.rs at rust-v0.164.0. They do not implement
// arbitrary MCP forms or authorization URL elicitations.
func (c *conversation) reviewerMCPElicitation(ctx context.Context, req serverRequest) (any, error) {
	var p struct {
		ThreadID   string                     `json:"threadId"`
		TurnID     string                     `json:"turnId"`
		ServerName string                     `json:"serverName"`
		Mode       string                     `json:"mode"`
		Message    string                     `json:"message"`
		Meta       map[string]json.RawMessage `json:"_meta"`
		Schema     struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		} `json:"requestedSchema"`
	}
	scope := c.reviewerApproval.Load()
	if scope == nil || c.disableNetwork || json.Unmarshal(req.Params, &p) != nil {
		return nil, errors.New("MCP approval requires a scoped reviewer")
	}
	var kind string
	_ = json.Unmarshal(p.Meta["codex_approval_kind"], &kind)
	c.mu.Lock()
	current := c.reviewerRequestCurrent(p.ThreadID, p.TurnID)
	c.mu.Unlock()
	if !current || rawID(req.ID) == "" || p.Mode != "form" || kind != "mcp_tool_call" || p.ServerName == "" || p.Message == "" || p.Schema.Type != "object" || p.Schema.Properties == nil || len(p.Schema.Properties) != 0 || len(p.Schema.Required) != 0 {
		return nil, errors.New("unsupported or stale MCP reviewer approval form")
	}
	detail, _ := json.Marshal(map[string]any{
		"method": req.Method, "server": p.ServerName, "reason": p.Message,
		"arguments": p.Meta["tool_params"],
	})
	return c.awaitApproval(ctx, req, domain.ActivityKindMCPTool, reviewerApprovalOptions("accept", "decline", "cancel"), p.Message, reviewerApprovalDetail(detail, *scope), p.TurnID, scope, func(decision ports.ChatDecision) any {
		// Omitting _meta.persist is significant: native interprets session/always
		// metadata as a lasting grant. Content cannot supply another decision.
		return map[string]any{"action": decision.ID, "content": nil}
	})
}

func (c *conversation) reviewerMCPInput(ctx context.Context, req serverRequest) (any, bool, error) {
	parked, _, err := parseUserInput(req.Params)
	if err != nil {
		return nil, true, err
	}
	questions := parked.params.Questions
	isApproval := false
	for _, question := range questions {
		isApproval = isApproval || strings.HasPrefix(question.ID, "mcp_tool_call_approval_")
	}
	if !isApproval {
		return nil, false, nil
	}
	scope := c.reviewerApproval.Load()
	if scope == nil {
		return nil, true, errors.New("reviewer approval scope ended")
	}
	c.mu.Lock()
	current := c.reviewerRequestCurrent(parked.params.ThreadID, parked.params.TurnID)
	c.mu.Unlock()
	if !current || len(questions) != 1 || rawID(req.ID) == "" || c.disableNetwork {
		return nil, true, errors.New("invalid MCP reviewer input approval scope")
	}
	question := questions[0]
	allow, cancel := false, false
	for _, option := range question.Options {
		allow = allow || option.Label == "Allow"
		cancel = cancel || option.Label == "Cancel"
	}
	if !allow || !cancel || (question.IsOther != nil && *question.IsOther) {
		return nil, true, errors.New("unsupported MCP reviewer approval options")
	}
	detail, _ := json.Marshal(map[string]any{"method": req.Method, "reason": question.Question, "questionId": question.ID})
	result, err := c.awaitApproval(ctx, req, domain.ActivityKindMCPTool, reviewerApprovalOptions("accept", "cancel"), question.Question, reviewerApprovalDetail(detail, *scope), parked.params.TurnID, scope, func(decision ports.ChatDecision) any {
		answer := "Cancel"
		if decision.ID == "accept" {
			answer = "Allow"
		}
		return userInputReply{Answers: map[string]userInputAnswer{question.ID: {Answers: []string{answer}}}}
	})
	return result, true, err
}

func reviewerApprovalOptions(ids ...string) []ports.ChatDecisionOption {
	options := make([]ports.ChatDecisionOption, 0, len(ids))
	for _, id := range ids {
		raw, _ := json.Marshal(id)
		option, _ := decisionOption(raw)
		options = append(options, option)
	}
	return options
}
