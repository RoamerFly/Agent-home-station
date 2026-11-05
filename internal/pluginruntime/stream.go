package pluginruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"wechat-bot/internal/pluginhost"
)

// Stream is the bidirectional transport of a verified package's fixed
// entrypoint. It grants no command, argument, environment or mount overrides.
// The host owns protocol initialization and closes the stream on cancellation.
type Stream struct {
	cmd     *exec.Cmd
	input   io.WriteCloser
	output  io.ReadCloser
	done    chan struct{}
	once    sync.Once
	waitErr error
}

// StartStream reuses the exact Executor sandbox and registration policy. Raw
// administrator MCP command/env fields are not accepted by this API. Resolve
// must be a trusted installed-package resolver, just as for Executor.Start.
func (e *Executor) StartStream(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (*Stream, error) {
	cmd, cleanup, _, err := e.prepareCommand(ctx, p, g)
	if err != nil {
		return nil, err
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		cleanup()
		return nil, err
	}
	output, childOutput, err := os.Pipe()
	if err != nil {
		input.Close()
		cleanup()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = childOutput, nil
	if err = ctx.Err(); err == nil {
		err = cmd.Start()
	}
	if err != nil {
		input.Close()
		output.Close()
		childOutput.Close()
		cleanup()
		return nil, errors.Join(ctx.Err(), errors.New("plugin sandbox stream start failed"))
	}
	childOutput.Close()
	stream := &Stream{cmd: cmd, input: input, output: output, done: make(chan struct{})}
	go func() { stream.waitErr = cmd.Wait(); cleanup(); close(stream.done) }()
	return stream, nil
}

func (s *Stream) Stdin() io.WriteCloser { return s.input }
func (s *Stream) Stdout() io.ReadCloser { return s.output }
func (s *Stream) Wait() error           { <-s.done; return s.waitErr }
func (s *Stream) Close() error {
	s.once.Do(func() { _ = s.input.Close(); _ = s.output.Close(); terminate(s.cmd) })
	select {
	case <-s.done:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("plugin sandbox stream termination unconfirmed")
	}
}
