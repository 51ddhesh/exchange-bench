package runner

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/51ddhesh/exchange-bench/internal/compiler"
	"github.com/coder/websocket"
)

const (
	networkName    = "exchange-bench-internal"
	runnerImage    = "exchange-bench-runner"
	contestantPort = "8080"
)

// Sandbox represents a running contestant container.
type Sandbox interface {
	Start(ctx context.Context, artifactPath, language string) error
	Endpoint() string
	Kill() error
	Wait() error
}

type dockerSandbox struct {
	seccompPath   string
	containerName string
	endpoint      string
}

// NewSandbox creates a Sandbox backed by the Docker CLI.
func NewSandbox(seccompPath string) Sandbox {
	return &dockerSandbox{seccompPath: seccompPath}
}

func (s *dockerSandbox) Start(ctx context.Context, artifactPath, language string) error {
	if err := ensureNetwork(ctx); err != nil {
		return err
	}

	absSeccomp, err := filepath.Abs(s.seccompPath)
	if err != nil {
		return fmt.Errorf("sandbox: resolve seccomp path: %w", err)
	}

	absArtifact, err := filepath.Abs(artifactPath)
	if err != nil {
		return fmt.Errorf("sandbox: resolve artifact path: %w", err)
	}

	lang, ok := compiler.Lookup(language)
	if !ok {
		return fmt.Errorf("sandbox: unsupported language %q", language)
	}

	s.containerName = uniqueContainerName(absArtifact)

	const containerArtifact = "/app/artifact"
	runCmd := lang.RunCmd(containerArtifact)

	// -p 0:8080 publishes contestant port to a random host port.
	// The host-side runner and smoke-test agent connect via localhost:{hostPort}.
	// Distributed workers (Step 4) are host processes on the same machine
	// and also use the published endpoint.
	args := []string{
		"run", "--rm", "-d",
		"--name", s.containerName,
		"--network", networkName,
		"--network-alias", "contestant",
		"-p", "0:" + contestantPort,
		"--read-only",
		"--tmpfs=/tmp:size=64m",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		fmt.Sprintf("--security-opt=seccomp=%s", absSeccomp),
		"--cpus=2",
		"--memory=512m",
		"--memory-swap=512m",
		"-v", absArtifact + ":" + containerArtifact + ":ro",
		runnerImage,
	}
	args = append(args, runCmd...)

	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("sandbox: docker run: %w\n%s", err, out)
	}

	port, err := s.publishedPort(ctx)
	if err != nil {
		s.Kill() //nolint:errcheck
		return err
	}

	s.endpoint = fmt.Sprintf("ws://localhost:%s/orders", port)

	if err := s.waitReady(ctx); err != nil {
		s.Kill() //nolint:errcheck
		return err
	}

	return nil
}

func (s *dockerSandbox) Endpoint() string { return s.endpoint }

func (s *dockerSandbox) Kill() error {
	return exec.Command("docker", "kill", s.containerName).Run()
}

func (s *dockerSandbox) Wait() error {
	exec.Command("docker", "wait", s.containerName).Run() //nolint:errcheck
	return nil
}

// publishedPort returns the host port Docker assigned for the container's :8080.
// Retries for up to 5 seconds to allow Docker to finish port binding after start.
func (s *dockerSandbox) publishedPort(ctx context.Context) (string, error) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.CommandContext(ctx, "docker", "inspect",
			"--format", `{{(index (index .NetworkSettings.Ports "8080/tcp") 0).HostPort}}`,
			s.containerName,
		).Output()
		if err == nil {
			if port := strings.TrimSpace(string(out)); port != "" && port != "<no value>" {
				return port, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("sandbox: host port not assigned after 5s (container may have exited)")
}

// waitReady polls the contestant's WebSocket endpoint until it accepts a
// connection or 10 seconds elapse.
func (s *dockerSandbox) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		conn, _, err := websocket.Dial(probeCtx, s.endpoint, nil)
		cancel()
		if err == nil {
			conn.Close(websocket.StatusNormalClosure, "probe") //nolint:errcheck
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("sandbox: contestant not ready within 10s at %s", s.endpoint)
}

// ensureNetwork creates exchange-bench-internal if it does not already exist.
func ensureNetwork(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "docker", "network", "create",
		"--driver", "bridge",
		"--label", "exchange-bench=true",
		networkName,
	).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("sandbox: create network %q: %w", networkName, err)
	}
	return nil
}

// uniqueContainerName produces a Docker-valid container name from the artifact path.
func uniqueContainerName(absArtifactPath string) string {
	parts := strings.Split(filepath.ToSlash(absArtifactPath), "/")
	for i := len(parts) - 2; i >= 0; i-- {
		p := parts[i]
		if p != "" && p != "exchange-bench" && p != "tmp" {
			return fmt.Sprintf("contestant-%s-%d", sanitizeName(p), time.Now().UnixNano()%1_000_000_000)
		}
	}
	return fmt.Sprintf("contestant-%d", time.Now().UnixNano())
}

// sanitizeName replaces characters invalid in Docker container names with hyphens.
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}
