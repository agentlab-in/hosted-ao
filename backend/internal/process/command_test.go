package process

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// TestCombinedOutputReturnsWhenAGrandchildOutlivesTheContext is the whole point
// of WaitDelay. `sh` exits when the context kills it, but the background child
// it spawned inherited the output pipe and keeps the write end open. Without
// WaitDelay, Wait blocks on that pipe for as long as the grandchild lives, so
// the caller's context deadline buys nothing and the goroutine leaks: this test
// hangs until the go test timeout rather than failing.
func TestCombinedOutputReturnsWhenAGrandchildOutlivesTheContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell to spawn a grandchild holding the pipe")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		// The backgrounded sleep inherits stdout/stderr, so it holds the pipe
		// open long past the deadline that kills the shell itself.
		_, _ = CombinedOutput(ctx, "sh", "-c", "sleep 60 & sleep 60")
		done <- time.Since(start)
	}()

	// Generous: the deadline plus WaitDelay plus room for a loaded CI box.
	budget := 200*time.Millisecond + WaitDelay + 10*time.Second
	select {
	case elapsed := <-done:
		if elapsed > budget {
			t.Fatalf("CombinedOutput took %s, want it bounded by the context plus WaitDelay", elapsed)
		}
	case <-time.After(budget):
		t.Fatal("CombinedOutput did not return: a grandchild is still holding the output pipe")
	}
}
