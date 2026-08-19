export interface Repository {
  id: number
  full_name: string
  clone_path: string
  last_indexed_sha: string
  created_at: string
  updated_at: string
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
    llm?: { status: string; latency_ms: number; provider: string }
  }
  ready: boolean
}

export interface SetupGitHubManifestResponse {
  manifest_flow: boolean
  manifest?: string
  state?: string
  url?: string
  explanation?: string
}

export interface SetupGitHubCallbackResponse {
  success: boolean
  app_id: number
  app_name: string
  message: string
}

export interface GitHubAppCredentials {
  app_id: number
  webhook_secret: string
  private_key_pem: string
  app_name?: string
  installation_id?: number
}

export interface LLMCredentials {
  provider: string
  gemini_api_key?: string
  ollama_api_key?: string
}

export interface SetupSaveCredentialsRequest {
  github?: GitHubAppCredentials
  llm?: LLMCredentials
}

export interface SetupTestLLMResponse {
  status: string
  provider: string
  detail: string
}

export interface SetupTestWebhookResponse {
  status: string
  app_id: number
  webhook_secret: boolean
  message: string
}

export interface AppConfig {
  ai: {
    llm_provider: string
    generator_model: string
  }
  github: {
    app_id: number
    webhook_configured: boolean
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
    high: number
    medium: number
    low: number
  }
  total_findings: number
  reviewed_at: string
  created_at: string
  revision: number
  is_re_review: boolean
}

export interface ReviewFinding {
  id: string
  severity: 'critical' | 'high' | 'medium' | 'low'
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
  history: ReviewHistoryItem[]
}

export interface ReviewHistoryItem {
  id: number
  head_sha: string
  created_at: string
  revision: number
  is_latest: boolean
  total_critical: number
}

export interface GlobalStats {
  total_repos: number
  total_reviews: number
  reviews_this_week: number
  total_findings: number
  findings_by_severity: {
    critical: number
    high: number
    medium: number
    low: number
  }
  avg_findings_per_review: number
  jobs_running: number
  jobs_queued: number
}

export interface JobRun {
  id: string
  type: 'review' | 'implement' | 'rereview'
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
  },

  setup: {
    status: () => fetchApi<SetupStatus>('/setup/status'),
    githubManifest: () =>
      fetchApi<SetupGitHubManifestResponse>('/setup/github/manifest', { method: 'POST' }),
    saveCredentials: (body: SetupSaveCredentialsRequest) =>
      fetchApi<{ status: string }>('/setup/credentials', {
        method: 'POST',
        body: JSON.stringify(body),
      }),
    testLLM: () =>
      fetchApi<SetupTestLLMResponse>('/setup/test-llm', { method: 'POST' }),
    testWebhook: () =>
      fetchApi<SetupTestWebhookResponse>('/setup/test-webhook', { method: 'POST' }),
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
    get: (repoId: number, prNumber: number, id?: number) =>
      fetchApi<ReviewDetail>(`/repos/${repoId}/reviews/${prNumber}${id ? `?id=${id}` : ''}`),
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
