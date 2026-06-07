package compiler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	compilerImage     = "exchange-bench-compiler"
	maxBinarySize     = 50 << 20 // 50 MB
	platformCargoToml = "deployments/docker/Cargo.toml"
	platformGoMod     = "deployments/docker/go.mod"
	platformGoSum     = "deployments/docker/go.sum"
)

// Compile compiles or stages the source file at sourcePath for the given
// language, writing the output artifact to outDir.
//
// Language-specific behaviour:
//   - cpp:    docker run exchange-bench-compiler with --network=none
//   - go:     platform go.mod + go.sum injected; go build .
//   - rust:   platform Cargo.toml injected; cargo build --release
//   - python: source staged directly, no Docker
func Compile(ctx context.Context, sourcePath, language, outDir string) (artifactPath, compilerOutput string, err error) {
	lang, ok := Lookup(language)
	if !ok {
		return "", "", fmt.Errorf("compiler: unsupported language %q", language)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", "", fmt.Errorf("compiler: create output dir: %w", err)
	}

	switch language {
	case "python":
		ext := filepath.Ext(sourcePath)
		dst := filepath.Join(outDir, "source"+ext)
		if err := stageFile(sourcePath, dst); err != nil {
			return "", "", fmt.Errorf("compiler: stage source: %w", err)
		}
		return dst, "", nil

	case "rust":
		return compileRust(ctx, sourcePath, outDir)

	case "go":
		return compileGo(ctx, sourcePath, outDir)

	default:
		return compileGeneric(ctx, lang, sourcePath, outDir)
	}
}

// compileGeneric handles cpp via a straightforward docker run.
func compileGeneric(ctx context.Context, lang Language, sourcePath, outDir string) (string, string, error) {
	srcDir := filepath.Dir(sourcePath)
	srcName := filepath.Base(sourcePath)

	containerSrc := "/src/" + srcName
	containerOut := "/out/binary"
	compileArgs := lang.CompileCmd(containerSrc, containerOut)

	dockerArgs := []string{
		"run", "--rm",
		"--network=none",
		"--workdir=/tmp",
		"--tmpfs=/tmp:size=512m,exec",
		"-v", srcDir + ":/src:ro",
		"-v", outDir + ":/out",
		compilerImage,
	}
	dockerArgs = append(dockerArgs, compileArgs...)

	out, runErr := runDocker(ctx, dockerArgs)
	if runErr != nil {
		return "", out, fmt.Errorf("compiler: compilation failed: %w", runErr)
	}

	return checkBinary(filepath.Join(outDir, "binary"), out)
}

// compileGo stages the platform go.mod + go.sum alongside the contestant
// source and invokes go build inside the compiler container.
//
// Layout inside the container:
//
//	/src/go.mod      (platform-owned, injected)
//	/src/go.sum      (platform-owned, injected)
//	/src/source.go   (contestant source)
//	/out/binary      (output)
func compileGo(ctx context.Context, sourcePath, outDir string) (string, string, error) {
	goSrcDir := filepath.Join(outDir, "go-src")
	if err := os.MkdirAll(goSrcDir, 0o755); err != nil {
		return "", "", fmt.Errorf("compiler: go staging dir: %w", err)
	}

	goModAbs, err := filepath.Abs(platformGoMod)
	if err != nil {
		return "", "", fmt.Errorf("compiler: resolve go.mod: %w", err)
	}
	if err := stageFile(goModAbs, filepath.Join(goSrcDir, "go.mod")); err != nil {
		return "", "", fmt.Errorf("compiler: stage go.mod: %w", err)
	}

	goSumAbs, err := filepath.Abs(platformGoSum)
	if err != nil {
		return "", "", fmt.Errorf("compiler: resolve go.sum: %w", err)
	}
	if err := stageFile(goSumAbs, filepath.Join(goSrcDir, "go.sum")); err != nil {
		return "", "", fmt.Errorf("compiler: stage go.sum: %w", err)
	}

	if err := stageFile(sourcePath, filepath.Join(goSrcDir, "source.go")); err != nil {
		return "", "", fmt.Errorf("compiler: stage source.go: %w", err)
	}

	absOutDir, err := filepath.Abs(outDir)
	if err != nil {
		return "", "", fmt.Errorf("compiler: resolve outDir: %w", err)
	}
	absGoSrcDir, err := filepath.Abs(goSrcDir)
	if err != nil {
		return "", "", fmt.Errorf("compiler: resolve goSrcDir: %w", err)
	}

	dockerArgs := []string{
		"run", "--rm",
		"--network=none",
		"--workdir=/src",
		"--tmpfs=/tmp:size=512m,exec",
		"-e", "GOPATH=/root/go",
		"-e", "GOCACHE=/tmp/gocache",
		"-e", "GOTOOLCHAIN=local",
		"-e", "GOPROXY=off",
		"-e", "GONOSUMDB=*",
		"-e", "GOMODCACHE=/root/go/pkg/mod",
		"-v", absGoSrcDir + ":/src:ro",
		"-v", absOutDir + ":/out",
		compilerImage,
		"go", "build", "-o", "/out/binary", ".",
	}

	out, runErr := runDocker(ctx, dockerArgs)
	if runErr != nil {
		return "", out, fmt.Errorf("compiler: go compilation failed: %w", runErr)
	}

	return checkBinary(filepath.Join(outDir, "binary"), out)
}

// compileRust sets up a Cargo workspace by copying the platform Cargo.toml
// and the contestant's main.rs into a temporary src/ layout, then invokes
// cargo build --release inside the compiler container.
//
// Layout inside the container:
//
//	/src/Cargo.toml      (platform-owned, injected)
//	/src/src/main.rs     (contestant source)
//	/out/binary          (output)
func compileRust(ctx context.Context, sourcePath, outDir string) (string, string, error) {
	rustSrcDir := filepath.Join(outDir, "rust-src")
	innerSrcDir := filepath.Join(rustSrcDir, "src")
	if err := os.MkdirAll(innerSrcDir, 0o755); err != nil {
		return "", "", fmt.Errorf("compiler: rust staging dir: %w", err)
	}

	cargoTomlAbs, err := filepath.Abs(platformCargoToml)
	if err != nil {
		return "", "", fmt.Errorf("compiler: resolve Cargo.toml: %w", err)
	}
	if err := stageFile(cargoTomlAbs, filepath.Join(rustSrcDir, "Cargo.toml")); err != nil {
		return "", "", fmt.Errorf("compiler: stage Cargo.toml: %w", err)
	}

	if err := stageFile(sourcePath, filepath.Join(innerSrcDir, "main.rs")); err != nil {
		return "", "", fmt.Errorf("compiler: stage main.rs: %w", err)
	}

	absOutDir, err := filepath.Abs(outDir)
	if err != nil {
		return "", "", fmt.Errorf("compiler: resolve outDir: %w", err)
	}
	absRustSrcDir, err := filepath.Abs(rustSrcDir)
	if err != nil {
		return "", "", fmt.Errorf("compiler: resolve rustSrcDir: %w", err)
	}

	// Copy source to writable /tmp/build — /src is mounted :ro and cargo
	// needs to write Cargo.lock. Build scripts also execute from
	// CARGO_TARGET_DIR which requires exec permission on the tmpfs.
	buildCmd := "cp -r /src /tmp/build && cargo build --release --manifest-path /tmp/build/Cargo.toml && cp /tmp/cargo-target/release/binary /out/binary"

	dockerArgs := []string{
		"run", "--rm",
		"--network=none",
		"--workdir=/tmp",
		"--tmpfs=/tmp:size=512m,exec",
		"-e", "CARGO_HOME=/root/.cargo",
		"-e", "CARGO_TARGET_DIR=/tmp/cargo-target",
		"-e", "CARGO_NET_OFFLINE=true",
		"-v", absRustSrcDir + ":/src:ro",
		"-v", absOutDir + ":/out",
		compilerImage,
		"sh", "-c", buildCmd,
	}

	out, runErr := runDocker(ctx, dockerArgs)
	if runErr != nil {
		return "", out, fmt.Errorf("compiler: rust compilation failed: %w", runErr)
	}

	return checkBinary(filepath.Join(outDir, "binary"), out)
}

// checkBinary verifies the output binary exists and is within size limits.
func checkBinary(path, compilerOut string) (string, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", compilerOut, fmt.Errorf("compiler: output binary missing: %w", err)
	}
	if info.Size() > maxBinarySize {
		os.Remove(path) //nolint:errcheck
		return "", compilerOut, fmt.Errorf("compiler: binary size %d bytes exceeds 50 MB limit", info.Size())
	}
	return path, compilerOut, nil
}

// runDocker runs docker with the given args and returns combined output.
func runDocker(ctx context.Context, args []string) (string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	return out.String(), cmd.Run()
}

// stageFile copies src to dst.
func stageFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
