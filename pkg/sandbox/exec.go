package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Result holds the execution outcome of a command run in the sandbox
type Result struct {
	Success bool
	Output  string
	GitDiff string
}

// RunCommand runs a test or compiler command in a shell inside the target directory.
// If the environment variable USE_DOCKER=true is set, it isolates execution inside a Docker container.
func RunCommand(ctx context.Context, dir string, cmdStr string, env []string) (*Result, error) {
	var cmd *exec.Cmd

	if os.Getenv("USE_DOCKER") == "true" {
		// Run isolated command inside Docker container
		args := []string{"run", "--rm", "-i"}
		// Mount target directory to container workspace
		args = append(args, "-v", fmt.Sprintf("%s:/workspace", dir), "-w", "/workspace")
		// Inject environment variables (resolved secrets)
		for _, eVal := range env {
			args = append(args, "-e", eVal)
		}
		// Use official Golang alpine image and execute command
		args = append(args, "golang:1.21-alpine", "sh", "-c", cmdStr)

		cmd = exec.CommandContext(ctx, "docker", args...)
	} else {
		// Local command execution
		cmd = exec.CommandContext(ctx, "sh", "-c", cmdStr)
		cmd.Dir = dir
		if len(env) > 0 {
			cmd.Env = append(os.Environ(), env...)
		}
	}

	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	err := cmd.Run()
	output := outBuf.String()

	// Capture the current git diff to observe changes
	diffCmd := exec.CommandContext(ctx, "git", "diff")
	diffCmd.Dir = dir
	var diffBuf bytes.Buffer
	diffCmd.Stdout = &diffBuf
	_ = diffCmd.Run() // Ignore errors if directory is not a git repo

	return &Result{
		Success: err == nil,
		Output:  output,
		GitDiff: diffBuf.String(),
	}, nil
}
