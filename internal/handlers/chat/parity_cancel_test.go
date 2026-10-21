package chat

import (
	"context"
	"errors"
	"testing"
)

func TestParityUpstreamCancelIsNotClientAbort(t *testing.T) {
	// A plain error whose TEXT claims a client abort is still not one: #455
	// removed the string matching for exactly that reason.
	for _, err := range []error{context.DeadlineExceeded,
		errors.New("upstream: client closed"), errors.New("upstream: broken pipe")} {
		if isClientCanceled(context.Background(), err) {
			t.Fatalf("misclassified %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !isClientCanceled(ctx, context.Canceled) {
		t.Fatal("client abort not detected")
	}
}
