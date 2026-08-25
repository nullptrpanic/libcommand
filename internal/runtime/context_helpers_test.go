package runtime

import (
	"context"
	"errors"
	"testing"
)

func TestContextCancellationAndNoopRestore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	execution, state := newNoOpExecutor(ctx, 10, &Request{})
	if status := execution.checkContext(state, unknownLocation); status != StatusIncomplete || !errors.Is(state.issue, context.Canceled) {
		t.Fatalf("checkContext() = %v, issue %v", status, state.issue)
	}
	noopRestore()
}
