package orchestrator

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ibreakthecloud/kiwi/pkg/provider"
	"github.com/ibreakthecloud/kiwi/pkg/sandbox"
	"github.com/ibreakthecloud/kiwi/pkg/tunnel"
)

// TaskState represents the status and progress of a cloud task
type TaskState struct {
	ID          string    `json:"id"`
	Task        string    `json:"task"`
	FilePath    string    `json:"file_path"`
	TestCmd     string    `json:"test_cmd"`
	Status      string    `json:"status"` // "RUNNING", "SUCCESS", "FAILED", "PAUSED"
	Logs        string    `json:"logs"`
	CreatedAt   time.Time `json:"created_at"`
	SandboxPath string    `json:"-"`
}

type Server struct {
	tasks      map[string]*TaskState
	tasksMutex sync.RWMutex
}

func NewServer() *Server {
	return &Server{
		tasks: make(map[string]*TaskState),
	}
}

// GenerateTaskID generates a short unique hex ID
func generateTaskID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/tasks", s.handleTasks)
	mux.HandleFunc("/tasks/", s.handleTaskStatus)
	mux.HandleFunc("/tunnel/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/response") {
			tunnel.HandleTunnelResponse(w, r)
		} else {
			tunnel.HandleTunnelConn(w, r)
		}
	})

	fmt.Printf("[Server] Kiwi daemon listening on %s\n", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse multipart form
	err := r.ParseMultipartForm(50 * 1024 * 1024) // 50MB max in memory
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse multipart form: %v", err), http.StatusBadRequest)
		return
	}

	task := r.FormValue("task")
	file := r.FormValue("file")
	testCmd := r.FormValue("test_cmd")

	if task == "" || file == "" || testCmd == "" {
		http.Error(w, "missing required parameters: task, file, test_cmd", http.StatusBadRequest)
		return
	}

	// Read codebase archive
	codebaseFile, _, err := r.FormFile("codebase")
	if err != nil {
		http.Error(w, "missing codebase file upload", http.StatusBadRequest)
		return
	}
	defer codebaseFile.Close()

	zipData, err := io.ReadAll(codebaseFile)
	if err != nil {
		http.Error(w, "failed to read uploaded codebase archive", http.StatusInternalServerError)
		return
	}

	// Create temporary sandbox directory
	tempSandbox, err := os.MkdirTemp("", "kiwi-server-sandbox-*")
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create temporary sandbox: %v", err), http.StatusInternalServerError)
		return
	}

	// Extract uploaded workspace
	err = sandbox.UnzipBytes(tempSandbox, zipData)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to extract codebase: %v", err), http.StatusInternalServerError)
		return
	}

	taskID := generateTaskID()
	state := &TaskState{
		ID:          taskID,
		Task:        task,
		FilePath:    file,
		TestCmd:     testCmd,
		Status:      "RUNNING",
		CreatedAt:   time.Now(),
		SandboxPath: tempSandbox,
	}

	s.tasksMutex.Lock()
	s.tasks[taskID] = state
	s.tasksMutex.Unlock()

	// Launch loop orchestration in the background
	go func() {
		logBuf := new(bytes.Buffer)
		p := provider.NewMockProvider()
		engine := NewEngine(p, 5)
		engine.LogOut = logBuf // Capture logs to task buffer
		engine.StateCallback = func(newStatus string) {
			s.tasksMutex.Lock()
			state.Status = newStatus
			s.tasksMutex.Unlock()
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		absFilePath := filepath.Join(tempSandbox, file)

		// Periodic status logger into buffer
		fmt.Fprintf(logBuf, "[Orchestrator] Spawned cloud sandbox at: %s\n", tempSandbox)

		err := engine.RunTask(ctx, taskID, tempSandbox, task, absFilePath, testCmd)

		s.tasksMutex.Lock()
		defer s.tasksMutex.Unlock()

		if err != nil {
			state.Status = "FAILED"
			fmt.Fprintf(logBuf, "\n[Execution Failure]: %v\n", err)
		} else {
			state.Status = "SUCCESS"
			fmt.Fprintln(logBuf, "\n[Execution Success]: Task completed successfully.")

			// In a real cloud flow, we would compress the fixed workspace back
			// so the local client can pull/extract it.
			// Let's store the final archive in the temporary directory.
			// The client can fetch it via another endpoint, or we zip it here.
			fixedZip, err := sandbox.ZipDir(tempSandbox)
			if err == nil {
				_ = os.WriteFile(filepath.Join(tempSandbox, "output.zip"), fixedZip, 0644)
			}
		}
		state.Logs = logBuf.String()
	}()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"task_id": taskID,
		"status":  "RUNNING",
	})
}

func (s *Server) handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	taskID := filepath.Base(r.URL.Path)

	s.tasksMutex.RLock()
	state, exists := s.tasks[taskID]
	s.tasksMutex.RUnlock()

	if !exists {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	// Serve the zip output if download=true
	if r.URL.Query().Get("download") == "true" {
		if state.Status != "SUCCESS" {
			http.Error(w, "Task not completed successfully or sandbox unavailable", http.StatusBadRequest)
			return
		}
		zipPath := filepath.Join(state.SandboxPath, "output.zip")
		fileBytes, err := os.ReadFile(zipPath)
		if err != nil {
			http.Error(w, "Result zip not found", http.StatusGone)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"kiwi-%s.zip\"", taskID))
		_, _ = w.Write(fileBytes)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(state)
}
