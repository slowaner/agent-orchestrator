package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewPublicationRouteFlagAndWire(t *testing.T) {
	command := newReviewTriggerCommand(&commandContext{})
	require.Equal(t, "auto", command.Flags().Lookup("publication-route").DefValue)
	require.NoError(t, command.ParseFlags([]string{"--publication-route", "glab", "--pr", "https://gitlab.example/g/p/-/merge_requests/1"}))
	selected, err := command.Flags().GetString("publication-route")
	require.NoError(t, err)
	require.Equal(t, "glab", selected)
	encoded, err := json.Marshal(triggerReviewRequest{PublicationRoute: selected})
	require.NoError(t, err)
	require.JSONEq(t, `{"publicationRoute":"glab"}`, string(encoded))
	var run reviewRun
	require.NoError(t, json.Unmarshal([]byte(`{"publicationRoute":"mcp"}`), &run))
	require.Equal(t, "mcp", run.PublicationRoute)
}
