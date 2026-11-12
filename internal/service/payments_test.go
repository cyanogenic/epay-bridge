package service

import (
	"testing"
	"time"
)

func TestRetryDelayIsCapped(t *testing.T) {
	if got := retryDelay(1); got != 5*time.Second {
		t.Fatalf("first delay = %v", got)
	}
	if got := retryDelay(100); got != 6*time.Hour {
		t.Fatalf("capped delay = %v", got)
	}
}
