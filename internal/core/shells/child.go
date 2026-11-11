package shells

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// Child is a spawned command, with its end told apart the way the registry
// needs: Exited is the shell process itself going away, Closed is its output
// pipe draining. They differ when the shell leaves something behind
// (`server &`): the shell is gone, but whatever it started still holds the
// pipe, is still writing, and is still in the process group to be killed.
//
// The output goes to a sink that can be swapped while the command runs. That
// is what lets a foreground command that outlives its timeout be handed to
// the registry without losing or duplicating a byte.
type Child struct {
	cmd    *exec.Cmd
	reader *os.File
	exited chan struct{}
	closed chan struct{}

	mu      sync.Mutex
	sink    io.Writer
	pending []byte // output that arrived before a sink was set
}

// SpawnOptions is what to run. Argv, when set, replaces the configured shell
// (a sandbox wrapper spawns its own).
type SpawnOptions struct {
	Command string
	CWD     string
	Argv    []string
	Sink    io.Writer
}

// Spawn starts a command in its own process group with stdout and stderr
// merged into the sink.
func Spawn(o SpawnOptions) (*Child, error) {
	if _, err := os.Stat(o.CWD); err != nil {
		return nil, fmt.Errorf("Working directory does not exist: %s\nCannot execute bash commands.", o.CWD)
	}
	argv := o.Argv
	if len(argv) == 0 {
		shell, args := ShellConfig()
		argv = append(append([]string{shell}, args...), o.Command)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = o.CWD
	cmd.Env = ShellEnv()
	// A real file, not an io.Writer: Wait then returns when the process
	// exits rather than when every holder of the pipe has let go.
	cmd.Stdout, cmd.Stderr = writer, writer
	setGroup(cmd)
	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	_ = writer.Close() // the child holds its own copy
	c := &Child{cmd: cmd, reader: reader, exited: make(chan struct{}), closed: make(chan struct{}), sink: o.Sink}
	track(c)
	go c.pump()
	go c.wait()
	return c, nil
}

func (c *Child) pump() {
	defer close(c.closed)
	defer c.reader.Close()
	buf := make([]byte, 32*1024)
	for {
		n, err := c.reader.Read(buf)
		if n > 0 {
			c.deliver(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (c *Child) deliver(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sink == nil {
		c.pending = append(c.pending, data...)
		return
	}
	_, _ = c.sink.Write(data)
}

func (c *Child) wait() {
	_ = c.cmd.Wait()
	close(c.exited)
}

// SetSink redirects the output from here on. Output that arrived while there
// was no sink is delivered first.
func (c *Child) SetSink(sink io.Writer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sink = sink
	if sink != nil && len(c.pending) > 0 {
		_, _ = sink.Write(c.pending)
		c.pending = nil
	}
}

// PID is the process id, which is also its process group.
func (c *Child) PID() int { return c.cmd.Process.Pid }

// Exited is closed once the process itself has gone.
func (c *Child) Exited() <-chan struct{} { return c.exited }

// Closed is closed once nothing holds the output pipe any more.
func (c *Child) Closed() <-chan struct{} { return c.closed }

// ExitCode is the exit status after Exited, or -1 for a signal.
func (c *Child) ExitCode() int { return c.cmd.ProcessState.ExitCode() }

// Signaled reports, after Exited, whether a signal ended the process.
func (c *Child) Signaled() bool { return signaled(c.cmd) }

// KillTree kills the process and everything in its group.
func (c *Child) KillTree() { killTree(c.PID()) }

// Release stops tracking the child: its output has drained, so the process
// group can no longer be trusted to be ours.
func (c *Child) Release() { untrack(c) }

// Detach lets go of a child whose command has finished: nothing more is read
// from it, even if something it started still holds the pipe.
func (c *Child) Detach() {
	_ = c.reader.Close()
	untrack(c)
}

// Every child still holding its pipe is tracked, so that leaving the process
// can take them all down — a foreground command cut off mid-run as much as a
// background shell.
var (
	trackedMu sync.Mutex
	tracked   = map[*Child]struct{}{}
)

func track(c *Child) {
	trackedMu.Lock()
	tracked[c] = struct{}{}
	trackedMu.Unlock()
}

func untrack(c *Child) {
	trackedMu.Lock()
	delete(tracked, c)
	trackedMu.Unlock()
}

func killTracked() {
	trackedMu.Lock()
	children := make([]*Child, 0, len(tracked))
	for c := range tracked {
		children = append(children, c)
	}
	tracked = map[*Child]struct{}{}
	trackedMu.Unlock()
	for _, c := range children {
		c.KillTree()
	}
}
