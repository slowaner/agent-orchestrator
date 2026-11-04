package domain

import "fmt"

// ReviewPublicationRoute selects reviewer instructions, not tool permissions or
// evidence that the reviewer actually used that transport.
type ReviewPublicationRoute string

const (
	// ReviewPublicationAuto keeps the project's available MCP/glab selection.
	ReviewPublicationAuto ReviewPublicationRoute = "auto"
	// ReviewPublicationMCP requires the existing project-supported MCP route.
	ReviewPublicationMCP ReviewPublicationRoute = "mcp"
	// ReviewPublicationGlab requires the existing project-supported glab route.
	ReviewPublicationGlab ReviewPublicationRoute = "glab"
)

// Normalized treats omitted legacy route values as auto.
func (r ReviewPublicationRoute) Normalized() ReviewPublicationRoute {
	if r == "" {
		return ReviewPublicationAuto
	}
	return r
}

// ValidateTarget requires explicit routes to name one complete GitLab MR URL.
// It does not contact a provider, select an account or grant access.
func (r ReviewPublicationRoute) ValidateTarget(prURL string) error {
	switch r.Normalized() {
	case ReviewPublicationAuto:
		return nil
	case ReviewPublicationMCP, ReviewPublicationGlab:
		ref, err := ParseChangeRequestURL(prURL)
		if err == nil && ref.Provider == "gitlab" {
			return nil
		}
		return fmt.Errorf("an explicit publication route requires one GitLab --pr URL")
	default:
		return fmt.Errorf("publication route must be auto, mcp or glab")
	}
}
