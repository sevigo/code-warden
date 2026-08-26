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
  },
}
