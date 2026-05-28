package exec

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/sirupsen/logrus"
)

// ExecCommandRunner is the real CommandRunner that shells out via os/exec.
type ExecCommandRunner struct{}

// NewExecCommandRunner returns a CommandRunner backed by os/exec.
func NewExecCommandRunner() *ExecCommandRunner {
	return &ExecCommandRunner{}
}

// Run executes the command and returns combined stdout+stderr.
func (r *ExecCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	logrus.Debugf("exec: %s %s", name, strings.Join(args, " "))
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("command %s failed: %w\noutput: %s", name, err, string(out))
	}
	return out, nil
}

// RunStreaming executes the command, streaming stdout/stderr to the provided writers.
func (r *ExecCommandRunner) RunStreaming(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	logrus.Debugf("exec (stream): %s %s", name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("command %s failed: %w", name, err)
	}
	return nil
}
