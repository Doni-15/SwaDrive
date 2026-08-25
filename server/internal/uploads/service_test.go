package uploads

import (
	"context"
	"testing"
	"time"
)

func TestCleanupWorkerStopsOnContextCancellation(t *testing.T) {
	service := &Service{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.RunCleanup(ctx, time.Hour); err != nil {
		t.Fatalf("RunCleanup(cancelled) error = %v", err)
	}
}
