package observe

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTriggeredDispatchRetainsWakeupDuringActivePoll(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	trigger := make(chan struct{}, 1)
	dispatches := make(chan PollDispatch, 2)
	release := make(chan struct{})
	done := StartTriggeredPollLoopWithDispatch(ctx, time.Hour, trigger, func(ctx context.Context, dispatch PollDispatch) error {
		dispatches <- dispatch
		if dispatch.Cause == "startup" {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return nil
	}, nil, "test")
	var first PollDispatch
	select {
	case first = <-dispatches:
	case <-time.After(time.Second):
		t.Fatal("startup poll did not run")
	}
	require.Equal(t, "startup", first.Cause)
	trigger <- struct{}{}
	close(release)
	select {
	case second := <-dispatches:
		require.Equal(t, "trigger", second.Cause)
		require.NotEmpty(t, second.ID)
		require.NotEqual(t, first.ID, second.ID)
	case <-time.After(time.Second):
		t.Fatal("pending trigger was lost")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poll loop did not stop")
	}
}
