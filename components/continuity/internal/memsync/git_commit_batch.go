package memsync

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// gitCommitBatch is owned by one locked refresh and is deliberately lazy: a
// warm lease receipt needs no commit-reader process. Requests are sequential,
// so neither a large input queue nor unbounded output buffering is possible.
// Close always cancels/reaps the child, including after malformed output. No
// goroutine, process, descriptor or object cache survives the refresh.
type gitCommitBatch struct {
	repo   git
	cmd    *exec.Cmd
	cancel context.CancelFunc
	input  io.WriteCloser
	pipe   io.ReadCloser
	output *bufio.Reader
	closed bool
}

func (b *gitCommitBatch) Read(ctx context.Context, oid string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.closed || !validGitOID(oid) {
		return nil, fmt.Errorf("invalid or closed Git commit batch")
	}
	if b.cmd == nil {
		child, cancel := context.WithCancel(ctx)
		b.cancel = cancel
		b.cmd = b.repo.command(child, []string{"cat-file", "--batch"})
		var err error
		b.input, err = b.cmd.StdinPipe()
		if err != nil {
			b.Close()
			return nil, err
		}
		output, err := b.cmd.StdoutPipe()
		if err != nil {
			b.Close()
			return nil, err
		}
		b.pipe = output
		// Do not retain Git diagnostics containing object contents or identities.
		b.cmd.Stderr = io.Discard
		if err := b.cmd.Start(); err != nil {
			b.Close()
			return nil, fmt.Errorf("start Git commit batch: %w", err)
		}
		b.output = bufio.NewReader(output)
		syncwork.RecordWork(ctx, "lease_commit_batch_processes", 1)
	}
	fail := func(err error) ([]byte, error) {
		b.Close()
		return nil, err
	}
	if _, err := io.WriteString(b.input, oid+"\n"); err != nil {
		return fail(fmt.Errorf("write Git commit batch request: %w", err))
	}
	// ReadSlice bounds headers to the reader's 4 KiB buffer. The declared
	// identity, exact type and size must be checked before allocating the body.
	header, err := b.output.ReadSlice('\n')
	if err != nil {
		return fail(fmt.Errorf("read Git commit batch header: %w", err))
	}
	fields := strings.Fields(string(header))
	if len(fields) != 3 || fields[0] != oid || fields[1] != "commit" {
		return fail(fmt.Errorf("Git commit batch returned missing or unexpected object"))
	}
	size, err := strconv.ParseUint(fields[2], 10, 32)
	if err != nil || size > maxTransportCommitBytes {
		return fail(fmt.Errorf("Git commit batch object exceeds bounds"))
	}
	body := make([]byte, int(size))
	if _, err := io.ReadFull(b.output, body); err != nil {
		return fail(fmt.Errorf("read Git commit batch body: %w", err))
	}
	if end, err := b.output.ReadByte(); err != nil || end != '\n' {
		return fail(fmt.Errorf("invalid Git commit batch terminator"))
	}
	// Independent content hashing also rejects replace/graft tricks and any
	// mismatch between a claimed batch header and the returned body.
	if _, err := decodeTransportCommit(oid, body); err != nil {
		return fail(err)
	}
	return body, nil
}

func (b *gitCommitBatch) Close() {
	if b.closed {
		return
	}
	b.closed = true
	if b.input != nil {
		_ = b.input.Close()
	}
	if b.cancel != nil {
		b.cancel()
	}
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Wait()
	}
	if b.pipe != nil {
		_ = b.pipe.Close()
	}
}
