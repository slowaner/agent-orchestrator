package codexappserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// recordReviewerPolicy uses the authoritative resolved thread response. Null raw
// config is not a default policy, and an explicit native deny cannot be widened.
func (c *conversation) recordReviewerPolicy(raw json.RawMessage, reviewer string, sandbox json.RawMessage, method string) error {
	scope := c.reviewerApproval.Load()
	if scope == nil {
		return nil
	}
	resolved, err := canonicalReviewerPolicy(raw)
	if err != nil {
		return fmt.Errorf("%s did not report a supported resolved reviewer approval policy: %w", method, err)
	}
	validSandbox, _ := reviewerSandboxNetwork(sandbox)
	if reviewer != "user" || !validSandbox {
		return errors.New("native reviewer did not resolve to human approvals and read-only sandbox")
	}
	c.reviewerSandbox = append(json.RawMessage(nil), sandbox...)
	if len(c.reviewerApprovalPolicy) > 0 {
		configured, err := canonicalReviewerPolicy(c.reviewerApprovalPolicy)
		if err != nil || (configured != resolved && resolved != "never") {
			return errors.New("resolved reviewer approval policy contradicts inherited configuration")
		}
		c.reviewerApprovalPolicy = append(json.RawMessage(nil), raw...)
	}
	c.log.Info("reviewer approval policy inherited", "review_id", scope.ReviewID, "run_id", scope.RunID, "approval_policy", string(raw), "origin_type", c.reviewerApprovalOrigin, "raw_override_present", len(c.reviewerApprovalPolicy) > 0, "native_response", method)
	return nil
}

func canonicalReviewerPolicy(raw json.RawMessage) (string, error) {
	if !json.Valid(raw) {
		return "", errors.New("malformed native approval policy")
	}
	var mode string
	if json.Unmarshal(raw, &mode) == nil {
		switch mode {
		case "untrusted", "on-request", "never":
			return mode, nil
		default:
			return "", errors.New("unknown native approval mode")
		}
	}
	var policy struct {
		Granular map[string]json.RawMessage `json:"granular"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil || policy.Granular == nil {
		return "", errors.New("malformed native approval policy")
	}
	flags := map[string]bool{"mcp_elicitations": false, "rules": false, "sandbox_approval": false, "request_permissions": false, "skill_approval": false}
	for key, value := range policy.Granular {
		if _, known := flags[key]; !known {
			return "", errors.New("unknown granular approval field")
		}
		var flag bool
		if err := json.Unmarshal(value, &flag); err != nil || isJSONNull(value) {
			return "", errors.New("invalid granular approval flag")
		}
		flags[key] = flag
	}
	for _, key := range []string{"mcp_elicitations", "rules", "sandbox_approval"} {
		if _, present := policy.Granular[key]; !present {
			return "", errors.New("missing granular approval flag")
		}
	}
	encoded, _ := json.Marshal(flags)
	return "granular:" + string(encoded), nil
}

// configureReviewerApproval removes only AO's noninteractive reviewer default.
// Native effective policy, including an explicit never or granular deny, remains
// authoritative. No approval decision or permission grant is made here.
func (c *conversation) configureReviewerApproval(ctx context.Context, scope *ports.ReviewerApprovalScope, readOnly, disableNetwork bool, cwd string) error {
	c.reviewerCwd = cwd
	if scope == nil {
		c.mu.Lock()
		c.reviewerApproval.Store(nil)
		c.mu.Unlock()
		c.reviewerApprovalPolicy = nil
		return nil
	}
	if !scope.Valid() || !readOnly || disableNetwork {
		return errors.New("invalid reviewer approval launch scope")
	}
	readCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	var result struct {
		Config struct {
			ApprovalPolicy json.RawMessage `json:"approval_policy"`
		} `json:"config"`
		Origins map[string]struct {
			Name struct {
				Type string `json:"type"`
			} `json:"name"`
		} `json:"origins"`
	}
	if err := c.conn.request(readCtx, "config/read", map[string]any{"cwd": cwd}, &result); err != nil {
		return fmt.Errorf("read inherited reviewer approval policy: %w", err)
	}
	if len(result.Config.ApprovalPolicy) > 0 && !isJSONNull(result.Config.ApprovalPolicy) {
		if _, err := canonicalReviewerPolicy(result.Config.ApprovalPolicy); err != nil {
			return fmt.Errorf("invalid inherited reviewer approval policy: %w", err)
		}
	} else {
		// config/read returns optional raw config, not resolved defaults. An
		// absent field must leave native inheritance in charge, never guess one.
		result.Config.ApprovalPolicy = nil
	}
	copyScope := *scope
	c.mu.Lock()
	c.reviewerApproval.Store(&copyScope)
	c.mu.Unlock()
	c.reviewerApprovalPolicy = append(json.RawMessage(nil), result.Config.ApprovalPolicy...)
	c.reviewerApprovalOrigin = result.Origins["approval_policy"].Name.Type
	return nil
}

func (c *conversation) applyReviewerApproval(params map[string]any) {
	if c.reviewerApproval.Load() == nil {
		return
	}
	if len(c.reviewerApprovalPolicy) == 0 {
		delete(params, "approvalPolicy")
	} else {
		params["approvalPolicy"] = c.reviewerApprovalPolicy
	}
	params["approvalsReviewer"] = "user"
	if _, turn := params["sandboxPolicy"]; turn {
		// A reconnected host may not have returned a resolved snapshot yet.
		// Unknown cannot inherit AO's legacy read-only network allowance.
		params["sandboxPolicy"] = map[string]any{"type": "readOnly", "networkAccess": false}
		if valid, _ := reviewerSandboxNetwork(c.reviewerSandbox); valid {
			params["sandboxPolicy"] = c.reviewerSandbox
		}
	}
}

func (c *conversation) reviewerRequestCurrent(threadID, turnID string) bool {
	return threadID == c.threadID && turnID != "" && turnID == c.activeTurn
}

// SetReviewerApprovalScope rebinds only an idle conversation between review
// passes. It never changes an active turn's scope or answers a pending request.
func (c *conversation) SetReviewerApprovalScope(ctx context.Context, scope *ports.ReviewerApprovalScope) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.mu.Lock()
	busy := c.closed || c.activeTurn != "" || len(c.pending) != 0 || len(c.pendingInputs) != 0
	c.mu.Unlock()
	if busy {
		return errors.New("reviewer approval scope cannot change during active work")
	}
	return c.configureReviewerApproval(ctx, scope, c.readOnly, c.disableNetwork, c.reviewerCwd)
}

func reviewerDecisionOptions(options []ports.ChatDecisionOption) []ports.ChatDecisionOption {
	filtered := make([]ports.ChatDecisionOption, 0, len(options))
	for _, option := range options {
		// Do not accept structured amendments hidden behind an otherwise familiar
		// option ID. The native string decision is the only approve-once encoding.
		var id string
		if json.Unmarshal(option.Raw, &id) == nil && (id == "accept" || id == "decline" || id == "cancel") {
			filtered = append(filtered, option)
		}
	}
	return filtered
}

func reviewerApprovalDetail(raw []byte, scope ports.ReviewerApprovalScope) []byte {
	var detail map[string]any
	if json.Unmarshal(raw, &detail) != nil {
		detail = make(map[string]any)
	}
	detail["reviewerScope"] = scope
	encoded, _ := json.Marshal(detail)
	return encoded
}

func newReviewerApprovalEpoch() string { return rand.Text() }

func (c *conversation) reviewerRequestID(id string) string {
	if scope := c.reviewerApproval.Load(); scope != nil {
		return "review:" + c.reviewerApprovalEpoch + ":" + scope.RunID + ":" + id
	}
	return id
}

func (c *conversation) nativeReviewerRequestID(id string) (string, bool) {
	if c.reviewerApproval.Load() == nil {
		return id, true
	}
	prefix := c.reviewerRequestID("")
	if !strings.HasPrefix(id, prefix) {
		return "", false
	}
	return strings.TrimPrefix(id, prefix), true
}

func reviewerSandboxNetwork(raw json.RawMessage) (valid, network bool) {
	var sandbox struct {
		Type          string `json:"type"`
		NetworkAccess *bool  `json:"networkAccess"`
	}
	if json.Unmarshal(raw, &sandbox) != nil || sandbox.Type != "readOnly" {
		return false, false
	}
	return true, sandbox.NetworkAccess != nil && *sandbox.NetworkAccess
}
