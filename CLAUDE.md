# Kiwi Development Guide (CLAUDE.md)

This file provides system context, build instructions, styling guidelines, and current status for AI assistants and contributors working on the **Kiwi** codebase.

---

## 1. Commands & Workflows

### Compilation & Signing (macOS workaround)
Because of the newer macOS `dyld` dynamic linker requirements (`missing LC_UUID load command`), Go binaries must be compiled with external linking and ad-hoc signed:

```bash
# Build Client CLI
go build -ldflags="-linkmode=external" -o kiwi cmd/kiwi/main.go
codesign -s - -f ./kiwi

# Build Server Daemon
go build -ldflags="-linkmode=external" -o kiwid cmd/kiwid/main.go
codesign -s - -f ./kiwid
```

### Running the Services
```bash
# Start the central cloud daemon with SQLite persistence and Docker sandboxing enabled
export USE_DOCKER="true"
export KIWI_SERVER_TOKEN="my-secret-token-1234"
./kiwid -addr :8080 -db kiwi.db

# Execute a TDD auto-fixing task using Bearer Token authorization
./kiwi -token "my-secret-token-1234" -task "Fix division by zero in Divide()" -file demo_project/math_utils.go -test-cmd "go test ./demo_project/..."

# Resume a paused cloud task (waiting for credentials tunnel reconnect)
./kiwi -token "my-secret-token-1234" -resume -task-id <task_id>
```

### Testing the Packages
```bash
# Run unit tests (CGO_ENABLED=0 bypasses macOS dyld UUID checks for tests)
CGO_ENABLED=0 go test -v ./pkg/...
```

---

## 2. Codebase Overview

```
kiwi/
├── cmd/
│   ├── kiwi/
│   │   └── main.go          # CLI client (zips codebase, auth headers, polls logs, downloads fixes)
│   └── kiwid/
│       └── main.go          # Cloud Daemon entrypoint (starts HTTP server, DB migrations)
├── pkg/
│   ├── orchestrator/
│   │   ├── engine.go        # Actor-Critic feedback loop orchestrator (caching resolver, safety gates)
│   │   ├── server.go        # HTTP tasks execution controller (/tasks, /tunnel, auth middleware)
│   │   └── db.go            # SQLite GORM persistence helper
│   ├── sandbox/
│   │   ├── exec.go          # Sandbox executor (local execution or isolated Docker container execution)
│   │   └── sync.go          # Workspace zip/unzip package with Zip Slip protection and database filtering
│   ├── provider/
│   │   └── mock.go          # Offline simulated LLM Actor-Critic rules
│   ├── tunnel/
│   │   └── tunnel.go        # Reverse credential tunnel multiplexer with memory caching
│   └── dashboard/
│       └── dashboard.go     # Embedded HTML/JS Kanban board UI
├── demo_project/            # A buggy Go project used for verification testing
└── go.mod
```

---

## 3. Current Implementation Status

*   **Phase 1 (Completed)**: Core local Actor-Critic Loop Engine that corrects local compilation/test failures using LLM edit mock rules.
*   **Phase 2 (Completed)**: Cloud Sandbox & Workspace Sync. Zips local codebase, uploads it to the daemon, runs execution loops inside sandbox directories, and syncs fixes back.
*   **Phase 3 (Completed)**: Reverse Credential Tunneling. Relays developer credentials (`GITHUB_TOKEN`) on-demand from local client to cloud sandbox.
*   **Phase 4 (Completed)**: Embedded Kanban Dashboard, Cost Budgets, & Loop Safety:
    *   Sleek dark-mode dashboard hosted at `http://localhost:8080/dashboard`.
    *   Semantic duplicate-error checker halts loop if compile error occurs 3 times.
    *   Cost capping stops execution if task exceeds `$0.20` budget cap.
*   **Phase 5 (Completed)**: Authentication & Docker Sandboxing:
    *   **Bearer Token Authorization**: Enforces API access checks. Uses custom tokens (via `KIWI_SERVER_TOKEN`) to validate CLI requests.
    *   **Docker Container Sandboxing**: Spawns isolated `golang:1.21-alpine` containers to execute compiler and test commands, preventing host pollution.
    *   **Credentials Caching**: Caches resolved credentials in-memory on first fetch. Allows developers to close their laptops and sever the tunnel while the cloud loop continues executing to completion.

---

## 4. Current Limitations & TODOs

### Active Limitations
1.  **Mock AI Provider**: The LLM interface (`pkg/provider/mock.go`) uses rule-based simulations. Real integrations with Anthropic/OpenAI APIs are not yet wired up.
2.  **Local Secrets Store**: The CLI client reads local secrets from an unencrypted `secrets.json` file in the workspace or defaults to standard environment variables.
3.  **Local Host Mounted Sandboxes**: The Docker sandbox mode mounts directories from the host server (`/var/folders/...`) directly into the container. In multi-tenant systems, this leaks file descriptors and permissions, requiring independent Virtual Machine sandboxing.
4.  **In-Memory Tunnel Cache**: The credentials cache on the server is kept inside the local daemon's memory. If the daemon restarts, cached credentials for running tasks are lost, prompting a stateful pause until the developer reconnects.

### Remaining Work / TODOs

#### 1. Integration of Live LLM Providers (`pkg/provider/llm.go`)
*   `[ ]` Add OpenAI and Anthropic Go SDK clients.
*   `[ ]` Create prompt templates for the **Actor** (focused on resolving compiler errors and writing functional code edits) and the **Critic** (reviews diffs for performance, safety, and correctness).
*   `[ ]` Support dynamic model swapping (e.g., GPT-4o for Actor, Claude 3.5 Sonnet for Critic).

#### 2. PKCE-based OAuth 2.0 Auth Flow
*   `[ ]` Implement a `kiwi login` command in the CLI. Spawns a temporary local HTTP server, opens the browser to authenticate via Auth0/Okta, and obtains JWT tokens.
*   `[ ]` Secure JWT storage on the developer's client machine using platform-specific keychains (e.g., `keyring` in Linux, Keychain in macOS).
*   `[ ]` Integrate JWT signature verification middleware on the server to replace the static token checks.

#### 3. Interactive Web Dashboard Controls (`pkg/dashboard/`)
*   `[ ]` Expose task cancellation (`POST /tasks/{id}/cancel`) and manual pausing (`POST /tasks/{id}/pause`) HTTP endpoints.
*   `[ ]` Update the Kanban UI with buttons to:
    *   Pause/Resume execution loops.
    *   Trigger new task submissions via a modal.
    *   Interact with human-in-the-loop checks (e.g., prompt for permission to apply a critical code fix).

#### 4. Sandbox Virtualization & Isolation Manager
*   `[ ]` Move from simple Docker mounts to a dedicated MicroVM orchestration layer (e.g. **Firecracker** or **gVisor**).
*   `[ ]` Implement a Sandbox Manager service that dynamically provisions clean kernel-isolated sandboxes in <100ms.
*   `[ ]` Support custom execution base images mapped per programming language (Go, Python, Node.js).

#### 5. High-Performance Log Streaming
*   `[ ]` Move live streaming logs from SQLite/GORM updates to **Redis Streams** or WebSocket events.
*   `[ ]` Save archived logs to an object store (e.g. AWS S3, Google Cloud Storage) upon task completion to keep the relational database clean and scalable.

