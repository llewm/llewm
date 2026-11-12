package memsync

import "os/exec"

// gitCommand is a Git process whose cancellation reaches every process Git
// starts, not only git.exe. Start, Run and Wait must go through it rather than
// the embedded exec.Cmd, so the platform process tree is set up before Git runs
// and released once it is reaped.
type gitCommand struct {
	*exec.Cmd
	tree *processTree
}

func (c *gitCommand) Start() error { return c.tree.start(c.Cmd) }

func (c *gitCommand) Wait() error {
	err := c.Cmd.Wait()
	c.tree.release()
	return err
}

func (c *gitCommand) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}
