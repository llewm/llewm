package memsync

import "context"

type fetchResult struct {
	err   error
	probe bool
}

// fetchTask is owned by one cycle goroutine. Its worker touches only Git's
// remote refs and object store; it cannot import records or advance local HEAD.
// Every exit cancels and joins the worker before the clone lock is released.
type fetchTask struct {
	cancel context.CancelFunc
	done   <-chan fetchResult
	joined bool
	result fetchResult
}

func startFetchTask(ctx context.Context, run func(context.Context) fetchResult) *fetchTask {
	worker, cancel := context.WithCancel(ctx)
	done := make(chan fetchResult, 1)
	go func() { done <- run(worker) }()
	return &fetchTask{cancel: cancel, done: done}
}

func (f *fetchTask) wait() fetchResult {
	if !f.joined {
		f.result = <-f.done
		f.joined = true
	}
	return f.result
}

func (f *fetchTask) close() {
	f.cancel()
	f.wait()
}
