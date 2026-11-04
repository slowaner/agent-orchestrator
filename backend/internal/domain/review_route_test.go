package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewPublicationRouteValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		route  ReviewPublicationRoute
		target string
		valid  bool
	}{
		{"legacy", "", "", true},
		{"auto github", ReviewPublicationAuto, "https://github.com/o/r/pull/1", true},
		{"mcp subgroup", ReviewPublicationMCP, "https://gitlab.example/g/sub/p/-/merge_requests/12", true},
		{"glab subgroup", ReviewPublicationGlab, "https://gitlab.example/g/sub/p/-/merge_requests/12", true},
		{"missing target", ReviewPublicationGlab, "", false},
		{"github", ReviewPublicationMCP, "https://github.com/o/r/pull/1", false},
		{"invalid route", "shell", "https://gitlab.example/g/p/-/merge_requests/1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.route.ValidateTarget(tc.target)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	require.Equal(t, ReviewPublicationAuto, ReviewPublicationRoute("").Normalized())
}
