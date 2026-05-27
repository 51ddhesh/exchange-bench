package runner

import (
	"bufio"
	"context"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// Sandbox is the interface the runner depends on.
// The only operations the dispatch loop needs: write to stdin, read from
// stdout, and kill the process when done or on error.
// Keeping this as an interface makes runner_test.go independent of Docker.
type Sandbox interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Kill() error
}

// dockerSandbox is the production Sandbox backed by a Docker container.
// One instance per evaluation run. Not reused between runs.
type dockerSandbox struct {
	cli         *client.Client
	containerID string
	stdin       io.WriteCloser
	stdout      io.ReadCloser
}

// StartSandbox creates, starts, and attaches to a hardened Docker container
// running the given image. Returns a ready-to-use Sandbox whose Stdin and
// Stdout are wired to the container's PID 1 stdio.
//
// Hardening applied:
//   - No network access (NetworkMode: none)
//   - Read-only root filesystem
//   - 64 MB tmpfs at /tmp (the only writable path)
//   - 2 CPUs, 512 MB memory, no swap
//   - All capabilities dropped
//   - no-new-privileges and seccomp profile enforced
//   - AutoRemove: container is deleted the moment it exits
func StartSandbox(ctx context.Context, image string) (Sandbox, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}

	cfg := &container.Config{
		Image:        image,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: false,
		OpenStdin:    true,
		StdinOnce:    true, // close stdin when the attach session ends
	}

	hostCfg := &container.HostConfig{
		NetworkMode:    "none",
		ReadonlyRootfs: true,
		Tmpfs:          map[string]string{"/tmp": "size=64m"},
		AutoRemove:     true,
		CapDrop:        []string{"ALL"},
		SecurityOpt: []string{
			"no-new-privileges:true",
			"seccomp=deployments/docker/seccomp/contestant.json",
		},
		Resources: container.Resources{
			NanoCPUs:   2 * 1e9, // 2.0 CPUs
			Memory:     512 * 1024 * 1024,
			MemorySwap: 512 * 1024 * 1024, // equal to Memory = no swap
		},
	}

	created, err := cli.ContainerCreate(ctx, cfg, hostCfg, &network.NetworkingConfig{}, nil, "")
	if err != nil {
		return nil, err
	}

	// Attach before starting so we don't miss any early output.
	attachResp, err := cli.ContainerAttach(ctx, created.ID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: false,
	})
	if err != nil {
		return nil, err
	}

	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		attachResp.Close()
		return nil, err
	}

	return &dockerSandbox{
		cli:         cli,
		containerID: created.ID,
		stdin:       attachResp.Conn,   // hijacked conn satisfies io.WriteCloser
		stdout: &bufReadCloser{
			Reader: attachResp.Reader, // buffered reader over the same conn
			Closer: attachResp.Conn,
		},
	}, nil
}

func (s *dockerSandbox) Stdin() io.WriteCloser { return s.stdin }
func (s *dockerSandbox) Stdout() io.ReadCloser { return s.stdout }

// Kill sends SIGKILL to the container. AutoRemove handles filesystem cleanup.
func (s *dockerSandbox) Kill() error {
	return s.cli.ContainerKill(context.Background(), s.containerID, "SIGKILL")
}

// bufReadCloser wraps a *bufio.Reader with an io.Closer so it satisfies io.ReadCloser.
type bufReadCloser struct {
	*bufio.Reader
	io.Closer
}
