export interface Repository {
  id: number
  full_name: string
  clone_path: string
  qdrant_collection_name: string
  embedder_model_name: string
  last_indexed_sha: string
  created_at: string
  updated_at: string
}

export interface ScanState {
  id: number
  repository_id: number
  // 'scanning'/'in_progress'/'pending' from web or prescan CLI; 'not_indexed' is UI-only (no scan state)
  status: 'pending' | 'in_progress' | 'scanning' | 'completed' | 'failed' | 'not_indexed'
  progress?: {
    files_total: number
    files_done: number
    stage: string
    current_file?: string
  }
  artifacts?: {
    chunks_count: number
    indexed_at: string
  }
  created_at: string
  updated_at: string
}

export interface RepoStats {
  chunks_count: number
  files_count: number
  last_indexed_sha: string
  last_scan_date: string
}

export interface ChatRequest {
  question: string
  history: string[]
}

export interface ChatResponse {
  answer: string
}

export interface ExplainRequest {
  path: string
}

export interface ExplainResponse {
  content: string
}

export interface RegisterRepoRequest {
  clone_path: string
  full_name: string
}

const API_BASE = '/api/v1'

async function fetchApi<T>(endpoint: string, options?: RequestInit): Promise<T> {
  const response = await fetch(`${API_BASE}${endpoint}`, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...options?.headers,
    },
  })

  if (!response.ok) {
    const error = await response.json().catch(() => ({ message: 'An error occurred' }))
    throw new Error(error.message || `HTTP error ${response.status}`)
  }

  const text = await response.text()
  if (!text || text.trim() === 'null') {
    return null as T
  }

  return JSON.parse(text) as T
}

export const api = {
  repos: {
    list: () => fetchApi<Repository[]>('/repos'),
    get: (id: number) => fetchApi<Repository>(`/repos/${id}`),
    register: (data: RegisterRepoRequest) =>
      fetchApi<Repository>('/repos', {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    scan: (id: number) =>
      fetchApi<void>(`/repos/${id}/scan`, { method: 'POST' }),
    status: (id: number) =>
      fetchApi<ScanState | null>(`/repos/${id}/status`),
    stats: (id: number) =>
      fetchApi<RepoStats>(`/repos/${id}/stats`),
  },

  chat: {
    ask: (repoId: number, data: ChatRequest) =>
      fetchApi<ChatResponse>(`/repos/${repoId}/chat`, {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    explain: (repoId: number, data: ExplainRequest) =>
      fetchApi<ExplainResponse>(`/repos/${repoId}/explain`, {
        method: 'POST',
        body: JSON.stringify(data),
      }),
  },

  events: {
    scanProgress: (repoId: number): EventSource => {
      return new EventSource(`${API_BASE}/events?repo_id=${repoId}`)
    },
  },
}
