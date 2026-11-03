package codexappserver

// nativeMCPErrorOutcome recognizes only native denials known to occur before
// execution. It describes a tool error, not a verified publication receipt.
func nativeMCPErrorOutcome(message string) string {
	switch message {
	case "MCP tool call requires approval, but approval policy is never", "user rejected MCP tool call":
		return "pre_execution_denial"
	default:
		return "unknown"
	}
}
