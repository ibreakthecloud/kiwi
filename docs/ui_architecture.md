# Standalone UI Dashboard Architecture

The current dashboard is an embedded Go string that lacks modern features, robust authentication, and separation of concerns. This plan outlines the transition to a premium, standalone frontend service that mirrors and expands upon the CLI's capabilities.

## Proposed Tech Stack

*   **Framework:** **Vite + React + TypeScript**. A lightweight, extremely fast SPA framework.
*   **Styling:** **Vanilla CSS**. We will avoid generic utility frameworks and instead build a highly customized, premium design system. This will include glassmorphism, subtle micro-animations, vibrant gradients, and modern typography (e.g., Inter or Outfit) to ensure an absolute "wow" factor upon first load.
*   **State Management:** Context API or Zustand for lightweight global state (auth, active tasks).
*   **Real-time Communication:** Server-Sent Events (SSE) to consume logs from `kiwi-api`.

## Authentication & Identity

*   **OAuth / SSO:** We will implement **GitHub SSO (OIDC)** as the primary login mechanism.
*   **Token Flow:** The `kiwi-api` will act as the OAuth Relying Party. Upon successful GitHub login, the API will generate a scoped JWT and return it to the frontend.
*   **Multi-tenant Support:** Users will belong to Organizations, mirroring our backend GORM models, allowing for team-based budgeting and task viewing.

## Core Features (Replacing the CLI)

1.  **Task Submission (The "No-CLI" Flow):**
    *   *GitHub Integration:* Users can paste a GitHub repository URL and select a branch. The backend will clone it directly into the sandbox.
    *   *Local Uploads:* Using the HTML5 `<input type="file" webkitdirectory />` API, users can select a local folder in their browser. The frontend will compress it in-browser using a JS library (like JSZip) and upload it to the `/tasks` endpoint, exactly imitating the CLI's behavior.
2.  **Interactive Kanban Board:**
    *   Columns: Backlog, In Progress, Done, Paused/Failed.
    *   Auto-updating cards showing current task cost, agent state, and elapsed time.
3.  **Live Detail View:**
    *   A split-pane view displaying the active agent persona, the streaming SSE logs, and a live rendering of the Git diff as the agent writes code.
4.  **Billing & Settings:**
    *   A dedicated view for the VP of Engineering to track total LLM costs, set budget circuit breakers, and manage team API keys.
