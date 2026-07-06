package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/ibreakthecloud/kiwi/pkg/provider"
	"github.com/ibreakthecloud/kiwi/pkg/sandbox"
)

// Engine orchestrates the feedback loop (TDD actor-critic control loop)
type Engine struct {
	Provider provider.Provider
	MaxSteps int
	LogOut   io.Writer
}

// NewEngine creates a new Loop Orchestrator engine
func NewEngine(p provider.Provider, maxSteps int) *Engine {
	return &Engine{
		Provider: p,
		MaxSteps: maxSteps,
		LogOut:   os.Stdout,
	}
}

// Log writes formatted log messages to the configured output writer
func (e *Engine) log(format string, a ...interface{}) {
	if e.LogOut != nil {
		fmt.Fprintf(e.LogOut, format, a...)
	}
}

// RunTask starts the feedback loop to align the codebase to the desired state.
func (e *Engine) RunTask(ctx context.Context, dir string, task string, filePath string, testCmd string) error {
	e.log("[Orchestrator] Desired State: %s\n", task)
	e.log("[Orchestrator] Running initial test command: %s\n", testCmd)

	res, err := sandbox.RunCommand(ctx, dir, testCmd)
	if err != nil {
		return fmt.Errorf("sandbox failed to run command: %w", err)
	}

	if res.Success {
		e.log("[Orchestrator] Current state matches desired state. Tests already pass!\n")
		return nil
	}

	e.log("[Orchestrator] Tests failed. Entering correction loop...\n")
	e.log("[Sandbox Output]:\n%s\n", res.Output)

	for step := 1; step <= e.MaxSteps; step++ {
		e.log("\n=== Loop Iteration %d / %d ===\n", step, e.MaxSteps)

		// Read current source code
		content, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read target file: %w", err)
		}

		e.log("[Actor] Simulating edit proposal...\n")
		fixedCode, err := e.Provider.GetCodeEdit(ctx, task, filePath, string(content), res.Output)
		if err != nil {
			return fmt.Errorf("failed to get code edit: %w", err)
		}

		// Apply proposed patch/fix
		err = os.WriteFile(filePath, []byte(fixedCode), 0644)
		if err != nil {
			return fmt.Errorf("failed to write fix back to target file: %w", err)
		}
		e.log("[Actor] Applied proposed code edits to target file.\n")

		// Run compiler/tests in the sandbox
		e.log("[Sandbox] Re-running build/tests...\n")
		res, err = sandbox.RunCommand(ctx, dir, testCmd)
		if err != nil {
			return fmt.Errorf("sandbox run failed: %w", err)
		}

		// Critic phase
		if res.Success {
			e.log("[Critic] Success: Tests passed, compiler errors cleared.\n")
			return nil
		}

		e.log("[Critic] Fail: Target state still diverging.\n")
		e.log("[Sandbox Output]:\n%s\n", res.Output)
	}

	return fmt.Errorf("loop halted: reached max iterations (%d) without resolving task", e.MaxSteps)
}
