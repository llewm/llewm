package memsync

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFetchTaskOverlapsLocalWorkAndKeepsResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	want := errors.New("network result")
	f := startFetchTask(ctx, func(worker context.Context) fetchResult {
		defer close(finished)
		close(started)
		select {
		case <-release:
			return fetchResult{err: want, probe: true}
		case <-worker.Done():
			return fetchResult{err: worker.Err()}
		}
	})
	defer f.close()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Local work completes while the network worker is still blocked.
	select {
	case <-finished:
		t.Fatal("worker finished before local work")
	default:
	}
	close(release)
	for range 2 {
		if got := f.wait(); !errors.Is(got.err, want) || !got.probe {
			t.Fatalf("lost result: %+v", got)
		}
	}
	if ctx.Err() != nil {
		t.Fatal("worker canceled its caller")
	}
}

func TestFetchTaskCloseCancelsAndJoins(t *testing.T) {
	for _, parentCancel := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		finished := make(chan struct{})
		f := startFetchTask(ctx, func(worker context.Context) fetchResult {
			<-worker.Done()
			close(finished)
			return fetchResult{err: worker.Err()}
		})
		if parentCancel {
			cancel()
		}
		f.close()
		f.close()
		select {
		case <-finished:
		default:
			t.Fatal("close left the worker running")
		}
		if !errors.Is(f.wait().err, context.Canceled) {
			t.Fatal("lost cancellation")
		}
		if !parentCancel && ctx.Err() != nil {
			t.Fatal("cleanup canceled its caller")
		}
		cancel()
	}
}
