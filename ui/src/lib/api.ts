export interface Repository {
  id: number
  full_name: string
  clone_path: string
  qdrant_collection_name: string
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
  full_name: string
}

export interface SetupStatus {
  github_app: {
    configured: boolean
    app_id: number
    app_name: string
    install_url: string
  }
  services: {
    database: { status: string; latency_ms: number }
    qdrant: { status: string; latency_ms: number }
  }
  ready: boolean
}

export interface AppConfig {
  ai: {
    llm_provider: string
    generator_model: string
    embedder_model: string
  }
  github: {
    app_id: number
    webhook_configured: boolean
  }
  storage: {
    qdrant_host: string
  }
}

export interface ReviewSummary {
  id: number
  pr_number: number
  pr_title: string
  head_sha: string
  status: string
  severity_counts: {
    critical: number
    warning: number
    suggestion: number
  }
  total_findings: number
  reviewed_at: string
  created_at: string
}

export interface ReviewFinding {
  id: string
  severity: 'critical' | 'warning' | 'suggestion'
  category: string
  file: string
  line_start: number
  line_end: number
  title: string
  description: string
  suggestion: string
}

export interface ReviewDetail extends ReviewSummary {
  findings: ReviewFinding[]
}

export interface GlobalStats {
  total_repos: number
  indexed_repos: number
  total_reviews: number
  reviews_this_week: number
  total_findings: number
  findings_by_severity: {
    critical: number
    warning: number
    suggestion: number
  }
  avg_findings_per_review: number
  jobs_running: number
  jobs_queued: number
}

export interface JobRun {
  id: string
  type: 'review' | 'scan' | 'implement' | 'rereview'
  repo_full_name: string
  pr_number: number
  status: 'pending' | 'running' | 'completed' | 'failed'
  triggered_by: string
  triggered_at: string
  completed_at: string
  duration_ms: number
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

  setup: {
    status: () => fetchApi<SetupStatus>('/setup/status'),
  },

  config: {
    get: () => fetchApi<AppConfig>('/config'),
  },

  stats: {
    global: () => fetchApi<GlobalStats>('/stats/global'),
  },

  jobs: {
    list: (limit = 50, offset = 0) =>
      fetchApi<JobRun[]>(`/jobs?limit=${limit}&offset=${offset}`),
  },

  reviews: {
    list: (repoId: number) =>
      fetchApi<ReviewSummary[]>(`/repos/${repoId}/reviews`),
    get: (repoId: number, prNumber: number) =>
      fetchApi<ReviewDetail>(`/repos/${repoId}/reviews/${prNumber}`),
    feedback: (
      repoId: number,
      prNumber: number,
      data: { finding_id: string; verdict: string; note?: string }
    ) =>
      fetchApi<{ ok: boolean }>(`/repos/${repoId}/reviews/${prNumber}/feedback`, {
        method: 'POST',
        body: JSON.stringify(data),
      }),
  },
}
