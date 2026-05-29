package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const defaultSeccompPath = "deployments/docker/seccomp/contestant.json"

type Sandbox interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Kill() error
}

type cmdSandbox struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

// StartSandbox launches the contestant image via the docker CLI.
// Direct OS pipes replace the SDK hijacked-connection + stdcopy path,
// which has a multiplexed stream format that is fiddly to demux correctly.
func StartSandbox(ctx context.Context, image string, seccompPath string) (Sandbox, error) {
	absSeccomp, err := filepath.Abs(seccompPath)
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolve seccomp path: %w", err)
	}

	cmd := exec.CommandContext(ctx, "docker", "run",
		"--rm",
		"--interactive",
		"--network=none",
		"--read-only",
		"--tmpfs=/tmp:size=64m",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		fmt.Sprintf("--security-opt=seccomp=%s", absSeccomp),
		"--cpus=2",
		"--memory=512m",
		"--memory-swap=512m",
		image,
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("sandbox: stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("sandbox: stdout pipe: %w", err)
	}

	cmd.Stderr = os.Stderr // surface docker errors to worker terminal

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sandbox: docker run: %w", err)
	}

	return &cmdSandbox{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

func (s *cmdSandbox) Stdin() io.WriteCloser { return s.stdin }
func (s *cmdSandbox) Stdout() io.ReadCloser { return s.stdout }

func (s *cmdSandbox) Kill() error {
	if s.cmd.Process != nil {
		return s.cmd.Process.Kill()
	}
	return nil
}
