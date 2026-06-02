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
	// compilerImage is the Docker image used for all compilation steps.
	compilerImage = "exchange-bench-compiler"

	// maxBinarySize is the maximum permitted size of a compiled artifact.
	maxBinarySize = 50 << 20 // 50 MB
)

// Compile compiles or stages the source file at sourcePath for the given
// language, writing the output artifact to outDir.
//
// For compiled languages (cpp, rust, go), it runs the
// exchange-bench-compiler Docker image with --network=none.
// For interpreted languages (python), it copies the source file directly —
// no Docker invocation.
//
// Returns the absolute path of the produced artifact and the compiler's
// combined stdout+stderr (surfaced to contestants on failure).
// A non-nil error means compilation failed or the binary exceeded the 50 MB
// size cap.
func Compile(ctx context.Context, sourcePath, language, outDir string) (artifactPath, compilerOutput string, err error) {
	lang, ok := Lookup(language)
	if !ok {
		return "", "", fmt.Errorf("compiler: unsupported language %q", language)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", "", fmt.Errorf("compiler: create output dir: %w", err)
	}

	// Interpreted language: stage source file directly, no Docker.
	if lang.CompileCmd == nil {
		ext := filepath.Ext(sourcePath)
		dst := filepath.Join(outDir, "source"+ext)
		if err := stageFile(sourcePath, dst); err != nil {
			return "", "", fmt.Errorf("compiler: stage source: %w", err)
		}
		return dst, "", nil
	}

	// Compiled language: run inside exchange-bench-compiler container.
	srcDir := filepath.Dir(sourcePath)
	srcName := filepath.Base(sourcePath)

	containerSrc := "/src/" + srcName
	containerOut := "/out/binary"
	compileArgs := lang.CompileCmd(containerSrc, containerOut)

	// --workdir=/tmp ensures build tools that write to cwd (Zig .o files,
	// Rust temp artifacts) land in the tmpfs, not on the container rootfs.
	dockerArgs := []string{
		"run", "--rm",
		"--network=none",
		"--workdir=/tmp",
		"--tmpfs=/tmp:size=512m",
		"-v", srcDir + ":/src:ro",
		"-v", outDir + ":/out",
		compilerImage,
	}
	dockerArgs = append(dockerArgs, compileArgs...)

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	cmd.Stdout = &out
	cmd.Stderr = &out

	if runErr := cmd.Run(); runErr != nil {
		return "", out.String(), fmt.Errorf("compiler: compilation failed: %w", runErr)
	}

	artifact := filepath.Join(outDir, "binary")
	info, statErr := os.Stat(artifact)
	if statErr != nil {
		return "", out.String(), fmt.Errorf("compiler: output binary missing after compilation: %w", statErr)
	}
	if info.Size() > maxBinarySize {
		os.Remove(artifact) //nolint:errcheck
		return "", out.String(), fmt.Errorf("compiler: binary size %d bytes exceeds 50 MB limit", info.Size())
	}

	return artifact, out.String(), nil
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
