# UI TODO — Next Steps for Code Warden Web Interface

This file is written for any LLM agent picking up UI work. It describes concrete, prioritised tasks
with all the context needed to implement each one correctly without re-reading the whole codebase.

---

## 0. Current State (as of 2026-03-20)

The web UI is a React + Vite + shadcn/ui + TanStack Query v5 SPA served by the Go server.
Three routes exist:

| Route | File | Status |
|---|---|---|
| `/` | `src/routes/index.tsx` | Repo list, 3-col grid, search/filter |
| `/repos/:repoId` | `src/routes/repos.$repoId.tsx` | Detail page, stats, scan trigger |
| `/repos/:repoId/chat` | `src/routes/repos.$repoId.chat.tsx` | AI chat with markdown rendering |

The API client is at `src/lib/api.ts`. Backend API base is `/api/v1`.
All chat and explain calls have a 10-minute timeout both on the server (`router.go`) and the Vite dev proxy (`vite.config.ts`).

---

## 1. Fix: Real Scan Progress (BUG — top priority)

### Why it's broken

The scan progress bar exists in `repos.$repoId.tsx` (lines 181–199) but it **always shows indeterminate / 0%** for two reasons:

**Bug A — No intermediate progress writes in the web scan path.**
`TriggerScan` (in `internal/server/handler/webui.go`) calls `h.ragService.SetupRepoContext(...)`.
`SetupRepoContext` does not accept a progress callback, so the `scan_state.progress` column is never updated
while indexing runs. It jumps directly from `"scanning"` to `"completed"` with no intermediate writes.

The CLI prescan (`internal/prescan/scanner.go`) DOES write per-file progress using `stateMgr.SaveState(ctx, StatusInProgress, progress, nil)`, but this code path is not invoked by the web scan.

**Bug B — JSON field name mismatch.**
`internal/prescan/state.go Progress` uses `json:"total_files"` / `json:"processed_files"`.
`internal/server/handler/webui.go ProgressInfo` uses `json:"files_total"` / `json:"files_done"`.
These do not match, so even if the prescan wrote progress the UI struct would always deserialise as zeros.

### How to fix

**Step 1 — Align JSON tags.**
In `internal/prescan/state.go` change the struct tags:
```go
type Progress struct {
    TotalFiles     int             `json:"files_total"`      // was total_files
    ProcessedFiles int             `json:"files_done"`       // was processed_files
    Files          map[string]bool `json:"files"`
    LastUpdated    time.Time       `json:"last_updated"`
}
```
This makes prescan-written state readable by the UI response mapper (`toScanStateResponse`).

**Step 2 — Add a progress callback to `SetupRepoContext`.**
`rag.Service.SetupRepoContext` signature is in `internal/rag/service.go`.
Add an optional `ProgressFn func(done, total int, currentFile string)` parameter (or a struct option).
Inside the RAG indexer (`internal/rag/index/indexer.go`), call this function after each file is processed.
In `webui.go doScan`, pass a closure that calls `h.store.UpsertScanState(...)` with the updated counts.

**Step 3 — Write progress every N files, not every file.**
Writing to Postgres on every file will create contention for large repos. Batch-write every 10 files or every 5 seconds, whichever comes first.

**Step 4 — Fix hardcoded chunks_count = 0.**
In `webui.go doScan` line 324, `ArtifactsInfo{ChunksCount: 0}` is hardcoded.
After `SetupRepoContext` completes, query `store.GetScanState` to retrieve the artifact count, or expose a `CountChunks(collectionName) int` method on the vector store and store the result here.

### Frontend changes (none needed once backend is fixed)

The frontend already reads `scanState.progress.files_done / files_total` and shows the bar.
The only change needed is switching from polling to SSE (see §2 below).

---

## 2. Switch Scan Progress from Polling to SSE

### Current state

`repos.$repoId.tsx` uses `useQuery` with `refetchInterval: 2000` while scanning.
`src/lib/api.ts` already defines `api.events.scanProgress(repoId)` which returns an `EventSource`.
The backend SSE endpoint is `GET /api/v1/events?repo_id=...` — it pushes `event: scan` every 2 seconds
and auto-closes when status becomes `completed` or `failed` (see `webui.go SSEEvents`).

### What to implement

Replace the polling `useQuery` for `scanState` with a React hook that uses `EventSource` while scanning.

```tsx
// src/hooks/useScanProgress.ts
import { useState, useEffect, useRef } from 'react'
import type { ScanState } from '@/lib/api'
import { api } from '@/lib/api'

export function useScanProgress(repoId: number, enabled: boolean) {
  const [scanState, setScanState] = useState<ScanState | null>(null)
  const esRef = useRef<EventSource | null>(null)

  useEffect(() => {
    if (!enabled) return
    const es = api.events.scanProgress(repoId)
    esRef.current = es
    es.addEventListener('scan', (e) => {
      setScanState(JSON.parse((e as MessageEvent).data))
    })
    es.onerror = () => es.close()
    return () => es.close()
  }, [repoId, enabled])

  return scanState
}
```

In `repos.$repoId.tsx`, call `useScanProgress(id, isScanning)` and overlay the result on top of
the initial `useQuery` data.

---

## 3. Toast Notifications

There is no feedback when a scan finishes or fails while the user is on a different page.

### What to add

1. Install `sonner` (it's the shadcn/ui-recommended toast library): `npm install sonner`
2. Add `<Toaster />` to `src/components/Layout.tsx` (or `App.tsx`).
3. In `repos.$repoId.tsx`, watch `scanState.status` transitions and call `toast.success` / `toast.error`.
4. Optionally, maintain a background SSE connection per-repo from the repo list page so users on `/`
   get a toast when any scan completes.

---

## 4. Settings Page

There is currently no way to configure the AI provider, model names, or GitHub App credentials
from the UI. This is all done via `config.yaml`.

### Minimal useful settings UI

Route: `/settings`
Backend: a new `GET /api/v1/config` endpoint that returns read-only config values (model name, provider,
GitHub App ID) — **never expose secrets like the private key or webhook secret**.

The page should show:
- Current LLM provider (ollama / gemini) and model name
- Embedder model name
- GitHub App ID
- A "Test connection" button that calls a `POST /api/v1/config/test` endpoint

Full edit UI is out of scope — config-as-file is the intended workflow. Read-only display is enough.

---

## 5. Code Review Dashboard

The core feature of Code Warden is PR code reviews triggered via GitHub webhooks. The UI has no
visibility into review history.

### What to build

**Backend endpoints needed (none exist yet):**

```
GET  /api/v1/repos/:repoId/reviews          → paginated list of PullRequest + Review records
GET  /api/v1/repos/:repoId/reviews/:prNum   → single PR review detail with comments
```

Look at `internal/storage/database.go` — `GetReviewsByRepo` and related methods to see what data is available.

**Frontend routes:**

| Route | Purpose |
|---|---|
| `/repos/:repoId/reviews` | List of PRs that got reviews, with status badges |
| `/repos/:repoId/reviews/:prNum` | Full review: diff summary, per-file comments |

**RepoDetail page** (`repos.$repoId.tsx`): add a third action card "View Reviews" linking to the list.

---

## 6. GitHub App Onboarding / Webhook Setup Guide

New users don't know how to connect Code Warden to GitHub.
The empty state on the repo list page should guide them.

### What to add

When `repos` is an empty array, show an onboarding card instead of just the "Add Repository" button:

```
┌──────────────────────────────────────────────────────┐
│  Connect Code Warden to GitHub                       │
│                                                      │
│  1. Create a GitHub App  →  [link to docs]           │
│  2. Set GITHUB_APP_ID + GITHUB_PRIVATE_KEY_PATH      │
│  3. Install the App on your repo                     │
│  4. Add the repo here                                │
│                                                      │
│  Webhook URL: http://your-server/api/v1/webhook/github│
└──────────────────────────────────────────────────────┘
```

The webhook URL should be shown dynamically based on `window.location.origin`.

---

## 7. Repo List: Status-Aware Polling

The repo list page (`src/routes/index.tsx`) does not auto-refresh when a scan is in progress.
Users have to navigate away and back.

### Fix

In `index.tsx`, add `refetchInterval` to the repos query:

```tsx
const { data: repos } = useQuery<Repository[]>({
  queryKey: ['repos'],
  queryFn: () => api.repos.list(),
  refetchInterval: (query) => {
    // Refetch every 3s if any repo is currently scanning
    // Currently we'd need scan state for each repo — see §7a below
    return false
  },
})
```

**§7a — Scan status in list response (backend change needed).**
The `GET /api/v1/repos` response returns `Repository` objects which don't include scan status.
Add scan status to the list response or add a bulk-status endpoint:
```
GET /api/v1/repos/statuses → { [repoId]: ScanState }
```

---

## 8. Chat: Streaming Responses (Stretch Goal)

Currently the chat sends a POST and waits for the full LLM response before showing it.
For long answers this feels unresponsive even though the server is working.

### What to implement

**Backend:** Change `POST /repos/:repoId/chat` to use `text/event-stream` and stream tokens as the
LLM produces them. GoFrame's `llms.Model` interface has streaming support via callback options.
See `chains.WithStreamingFunc` in the goframe docs / source.

**Frontend:** Replace `fetchApi` chat call with a `ReadableStream` reader or `EventSource`.
Accumulate tokens into message state and re-render on each chunk.

This is a larger change affecting `internal/rag/question/qa.go`, the handler, and the React component.
Recommended to do this after §1 and §2 are solid.

---

## 9. Keyboard Shortcuts

The chat page already handles `Enter` / `Shift+Enter`. No other shortcuts exist.

Nice-to-haves:
- `Cmd+K` / `Ctrl+K`: open a command palette to jump to repos or start a chat
- `Escape`: close modals / dialogs
- `R` on repo detail: trigger re-scan (with guard against accidental press)

---

## 10. Error Boundary and Loading Skeletons

Currently a network error throws unhandled into React and shows a blank page.

- Add a top-level `<ErrorBoundary>` in `App.tsx` with a friendly "Something went wrong" fallback
- The repo detail page has a basic skeleton (`animate-pulse`). Make it match the actual layout more closely.
- The chat page has no loading state for the initial repo query — add one.

---

## API Reference (for implementing new features)

| Method | Path | Handler | Timeout |
|---|---|---|---|
| GET | `/api/v1/repos` | `ListRepos` | 30s |
| POST | `/api/v1/repos` | `RegisterRepo` | 30s |
| GET | `/api/v1/repos/:repoId` | `GetRepo` | 30s |
| POST | `/api/v1/repos/:repoId/scan` | `TriggerScan` | 30s |
| GET | `/api/v1/repos/:repoId/status` | `GetScanStatus` | 30s |
| GET | `/api/v1/repos/:repoId/stats` | `GetRepoStats` | 30s |
| POST | `/api/v1/repos/:repoId/chat` | `Chat` | 10m |
| POST | `/api/v1/repos/:repoId/explain` | `Explain` | 10m |
| GET | `/api/v1/events?repo_id=N` | `SSEEvents` | none (SSE) |

`ScanState.progress` shape expected by the UI (`src/lib/api.ts`):
```json
{
  "files_total": 120,
  "files_done": 45,
  "stage": "indexing",
  "current_file": "internal/rag/service.go"
}
```

`ScanState.status` values: `"pending"`, `"scanning"`, `"completed"`, `"failed"`, `"not_indexed"`.
