import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { motion } from 'framer-motion'
import {
  Shield,
  CheckCircle2,
  Circle,
  ExternalLink,
  Github,
  Database,
  Zap,
  AlertCircle,
  Cpu,
  KeyRound,
} from 'lucide-react'
import { api } from '@/lib/api'
import type {
  SetupStatus,
  SetupGitHubManifestResponse,
  SetupTestLLMResponse,
  LLMCredentials,
} from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { cn } from '@/lib/utils'

const stagger = { hidden: {}, show: { transition: { staggerChildren: 0.06 } } }
const fadeUp = { hidden: { opacity: 0, y: 8 }, show: { opacity: 1, y: 0, transition: { duration: 0.28 } } }

// ── Step Component ───────────────────────────────────────────────────────────

function Step({
  num,
  title,
  done,
  active,
  children,
}: {
  num: number
  title: string
  done: boolean
  active?: boolean
  children: React.ReactNode
}) {
  return (
    <motion.div variants={fadeUp} className="flex gap-4">
      <div className="flex flex-col items-center">
        <div className={cn(
          'h-8 w-8 rounded-full flex items-center justify-center text-sm font-bold shrink-0 transition-colors',
          done 
            ? 'bg-emerald-500/15 text-emerald-500' 
            : active 
              ? 'bg-blue-500/15 text-blue-500 ring-2 ring-blue-500/30'
              : 'bg-[#f1f2f3] text-[#8c919b] dark:bg-[#1e2025]'
        )}>
          {done ? <CheckCircle2 className="h-4 w-4" /> : num}
        </div>
        <div className="w-px flex-1 mt-2 bg-[#e1e3e6] dark:bg-[#2d2f36]" />
      </div>
      
      <div className="pb-8 flex-1 min-w-0">
        <p className={cn(
          'font-medium mb-2',
          done ? 'text-emerald-500' : active ? 'text-blue-500' : 'text-foreground'
        )}>
          {title}
        </p>
        <div className="text-sm text-[#8c919b] space-y-1">{children}</div>
      </div>
    </motion.div>
  )
}

// ── Service Pill Component ─────────────────────────────────────────────────────

function ServicePill({ ok, label }: { ok: boolean | undefined; label: string }) {
  if (ok === undefined) {
    return (
      <span className="inline-flex items-center gap-1.5 text-xs text-[#8c919b] bg-[#f1f2f3] dark:bg-[#1e2025] px-2.5 py-1 rounded-[5px]">
        <Circle className="h-3 w-3" /> {label}
      </span>
    )
  }
  return ok ? (
    <span className="inline-flex items-center gap-1.5 text-xs font-medium text-emerald-500 bg-emerald-500/10 px-2.5 py-1 rounded-[5px]">
      <CheckCircle2 className="h-3 w-3" /> {label} OK
    </span>
  ) : (
    <span className="inline-flex items-center gap-1.5 text-xs font-medium text-rose-500 bg-rose-500/10 px-2.5 py-1 rounded-[5px]">
      <Circle className="h-3 w-3" /> {label} unreachable
    </span>
  )
}

// ── Input helpers ──────────────────────────────────────────────────────────────

const inputClass =
  'w-full rounded-[5px] border border-[#d5d7db] dark:border-[#3b3d45] bg-white dark:bg-[#1e2025] px-3 py-2 text-sm text-foreground placeholder:text-[#8c919b] focus:outline-none focus:ring-2 focus:ring-blue-500/30'

function FieldLabel({ children }: { children: React.ReactNode }) {
  return <label className="block text-xs font-medium text-[#8c919b] mb-1.5">{children}</label>
}

// ── Main Page Component ──────────────────────────────────────────────────────────

export default function SetupPage() {
  const { data: status, refetch: refetchStatus } = useQuery<SetupStatus>({
    queryKey: ['setup-status'],
    queryFn: api.setup.status,
    refetchInterval: 10_000,
  })

  const githubConfigured = status?.github_app.configured ?? false
  const dbOk = status?.services.database.status === 'ok'
  const llmOk = status?.services.llm?.status === 'ok'
  const servicesOk = dbOk
  const allDone = githubConfigured && servicesOk && llmOk

  const getActiveStep = (): 1 | 2 | 3 | 4 => {
    if (!githubConfigured) return 1
    if (!servicesOk) return 2
    if (!llmOk) return 3
    return 4
  }

  const activeStep = getActiveStep()

  return (
    <div className="max-w-2xl mx-auto py-10 px-4">
      <Header />

      <ServiceStatusCard
        dbOk={dbOk}
        llmOk={llmOk}
        allDone={allDone}
        activeStep={activeStep}
        hasStatus={!!status}
      />

      <motion.div variants={stagger} initial="hidden" animate="show" className="space-y-0">
        <Step num={1} title="Create the GitHub App" done={githubConfigured} active={activeStep === 1}>
          <GitHubAppStep onConfigured={() => refetchStatus()} alreadyConfigured={githubConfigured} appName={status?.github_app.app_name} />
        </Step>

        <Step num={2} title="Install the GitHub App on your repos" done={!!status?.github_app.install_url} active={activeStep === 2}>
          {status?.github_app.install_url ? (
            <a
              href={status.github_app.install_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1.5 text-[#2264d6] hover:underline text-sm"
            >
              <Github className="h-4 w-4" />
              Install {status.github_app.app_name || 'the app'}
              <ExternalLink className="h-3 w-3" />
            </a>
          ) : (
            <p className="text-[#8c919b]">
              Once your app is configured in step 1, its install URL will appear here.
            </p>
          )}
        </Step>

        <Step num={3} title="Verify LLM connectivity" done={llmOk} active={activeStep === 3}>
          <LLMTestStep onResolved={() => refetchStatus()} provider={status?.services.llm?.provider} />
        </Step>

        <Step num={4} title="Trigger your first review" done={false} active={activeStep === 4}>
          <p>Install the app on your repositories, then open a PR and comment:</p>
          <div className="mt-2 inline-flex items-center gap-2 px-3 py-1.5 rounded-[6px] bg-[#2264d6]/10 text-[#2264d6] font-mono text-sm">
            <Zap className="h-3.5 w-3.5" />
            /review
          </div>
        </Step>
      </motion.div>

      {allDone && <SetupCompleteCard />}
    </div>
  )
}

// ── Header ─────────────────────────────────────────────────────────────────────

function Header() {
  return (
    <motion.div
      initial={{ opacity: 0, y: -20 }}
      animate={{ opacity: 1, y: 0 }}
      className="flex items-center gap-4 mb-10"
    >
      <div className="h-12 w-12 rounded-[8px] bg-[#2264d6]/10 flex items-center justify-center shrink-0">
        <Shield className="h-6 w-6 text-[#2264d6]" />
      </div>
      <div>
        <h1 className="text-2xl font-bold text-foreground">Setup Code Warden</h1>
        <p className="text-sm text-[#8c919b] mt-0.5">
          Complete the steps below to start reviewing pull requests
        </p>
      </div>
    </motion.div>
  )
}

// ── Service status card ───────────────────────────────────────────────────────

function ServiceStatusCard({
  dbOk,
  llmOk,
  allDone,
  activeStep,
  hasStatus,
}: {
  dbOk: boolean | undefined
  llmOk: boolean | undefined
  allDone: boolean
  activeStep: number
  hasStatus: boolean
}) {
  return (
    <Card className="mb-8 p-4">
      <div className="flex items-center gap-2 flex-wrap">
        <div className="flex items-center gap-2 text-sm">
          <Database className="h-4 w-4 text-[#8c919b]" />
          <ServicePill ok={dbOk} label="PostgreSQL" />
        </div>
        <div className="w-px h-4 bg-[#e1e3e6] dark:bg-[#2d2f36] mx-1" />
        <div className="flex items-center gap-2 text-sm">
          <Cpu className="h-4 w-4 text-[#8c919b]" />
          <ServicePill ok={llmOk} label="LLM" />
        </div>
        {hasStatus && (
          <span className={cn(
            "text-xs ml-auto font-medium",
            allDone ? 'text-emerald-500' : 'text-amber-500'
          )}>
            {allDone ? '✓ All systems go' : `${activeStep} of 4 steps complete`}
          </span>
        )}
      </div>
    </Card>
  )
}

// ── Step 1: GitHub App ─────────────────────────────────────────────────────────

function GitHubAppStep({
  alreadyConfigured,
  appName,
  onConfigured,
}: {
  alreadyConfigured: boolean
  appName?: string
  onConfigured: () => void
}) {
  // Manifest flow
  const manifestMutation = useMutation<SetupGitHubManifestResponse, Error>({
    mutationFn: api.setup.githubManifest,
    onSuccess: (data) => {
      if (data.manifest_flow && data.url) {
        // Redirect the user to GitHub. They'll come back to our callback URL,
        // which writes the credentials and redirects back to /setup.
        window.location.href = data.url
      }
    },
  })

  // Manual credentials form state
  const [manualMode, setManualMode] = useState(false)
  const [appId, setAppId] = useState('')
  const [webhookSecret, setWebhookSecret] = useState('')
  const [privateKeyPem, setPrivateKeyPem] = useState('')
  const [formError, setFormError] = useState<string | null>(null)
  const [formSuccess, setFormSuccess] = useState<string | null>(null)

  const saveMutation = useMutation({
    mutationFn: api.setup.saveCredentials,
    onSuccess: () => {
      setFormSuccess('GitHub App credentials saved.')
      setFormError(null)
      onConfigured()
    },
    onError: (err: Error) => {
      setFormError(err.message)
      setFormSuccess(null)
    },
  })

  if (alreadyConfigured) {
    return (
      <div className="flex items-center gap-2 text-emerald-600">
        <CheckCircle2 className="h-4 w-4" />
        <span>
          App <strong>{appName || 'GitHub App'}</strong> is configured. You can re-run setup below if needed.
        </span>
      </div>
    )
  }

  if (manualMode) {
    const submit = (e: React.FormEvent) => {
      e.preventDefault()
      setFormError(null)
      setFormSuccess(null)
      const appIdNum = parseInt(appId, 10)
      if (!appIdNum || !webhookSecret || !privateKeyPem) {
        setFormError('App ID, webhook secret, and private key PEM are all required.')
        return
      }
      saveMutation.mutate({
        github: {
          app_id: appIdNum,
          webhook_secret: webhookSecret,
          private_key_pem: privateKeyPem,
        },
      })
    }

    return (
      <form onSubmit={submit} className="space-y-3">
        <p className="text-[#8c919b]">
          Paste the values from your GitHub App's settings page. They will be stored encrypted in the database.
        </p>
        <div>
          <FieldLabel>App ID</FieldLabel>
          <input
            className={inputClass}
            value={appId}
            onChange={(e) => setAppId(e.target.value)}
            placeholder="123456"
            inputMode="numeric"
          />
        </div>
        <div>
          <FieldLabel>Webhook secret</FieldLabel>
          <input
            className={inputClass}
            type="password"
            value={webhookSecret}
            onChange={(e) => setWebhookSecret(e.target.value)}
          />
        </div>
        <div>
          <FieldLabel>Private key (PEM)</FieldLabel>
          <textarea
            className={cn(inputClass, 'font-mono text-xs h-32 resize-y')}
            value={privateKeyPem}
            onChange={(e) => setPrivateKeyPem(e.target.value)}
            placeholder="-----BEGIN RSA PRIVATE KEY-----&#10;...&#10;-----END RSA PRIVATE KEY-----"
          />
        </div>
        {formError && (
          <p className="text-xs text-rose-500 flex items-center gap-1">
            <AlertCircle className="h-3 w-3" /> {formError}
          </p>
        )}
        {formSuccess && (
          <p className="text-xs text-emerald-500 flex items-center gap-1">
            <CheckCircle2 className="h-3 w-3" /> {formSuccess}
          </p>
        )}
        <div className="flex gap-2">
          <Button type="submit" loading={saveMutation.isPending}>
            Save credentials
          </Button>
          <Button type="button" variant="ghost" onClick={() => setManualMode(false)}>
            Back
          </Button>
        </div>
      </form>
    )
  }

  const manifestData = manifestMutation.data
  const needsManual = manifestData && !manifestData.manifest_flow

  return (
    <div className="space-y-3">
      <p>
        The wizard will create a GitHub App with the required permissions and webhook automatically.
        You'll be redirected to GitHub to approve.
      </p>
      <Button
        onClick={() => manifestMutation.mutate()}
        loading={manifestMutation.isPending}
      >
        <Github className="h-4 w-4 mr-1.5" /> Create GitHub App
      </Button>

      {needsManual && (
        <div className="rounded-[6px] border border-amber-500/40 bg-amber-500/5 p-3 text-xs text-amber-700 dark:text-amber-300">
          <div className="flex items-start gap-2">
            <AlertCircle className="h-4 w-4 mt-0.5 shrink-0" />
            <div className="space-y-2">
              <p>{manifestData?.explanation}</p>
              <button
                onClick={() => setManualMode(true)}
                className="inline-flex items-center gap-1 text-[#2264d6] hover:underline"
              >
                <KeyRound className="h-3 w-3" /> Enter credentials manually
              </button>
            </div>
          </div>
        </div>
      )}

      {manifestMutation.isError && !needsManual && (
        <p className="text-xs text-rose-500 flex items-center gap-1">
          <AlertCircle className="h-3 w-3" /> {manifestMutation.error?.message}
        </p>
      )}

      <div className="pt-2 text-xs">
        <button
          onClick={() => setManualMode(true)}
          className="text-[#8c919b] hover:text-foreground underline-offset-2 hover:underline"
        >
          I already have a GitHub App — enter credentials manually
        </button>
      </div>
    </div>
  )
}

// ── Step 3: LLM test ───────────────────────────────────────────────────────────

function LLMTestStep({
  onResolved,
  provider,
}: {
  onResolved: () => void
  provider?: string
}) {
  const [result, setResult] = useState<SetupTestLLMResponse | null>(null)
  const [geminiKey, setGeminiKey] = useState('')
  const [saveError, setSaveError] = useState<string | null>(null)

  const testMutation = useMutation<SetupTestLLMResponse, Error>({
    mutationFn: api.setup.testLLM,
    onSuccess: (data) => {
      setResult(data)
      if (data.status === 'ok') onResolved()
    },
  })

  const saveGemini = useMutation({
    mutationFn: (creds: LLMCredentials) => api.setup.saveCredentials({ llm: creds }),
    onSuccess: () => {
      setSaveError(null)
      testMutation.mutate()
    },
    onError: (err: Error) => setSaveError(err.message),
  })

  const isGemini = provider === 'gemini' || (testMutation.data?.provider === 'gemini')

  return (
    <div className="space-y-3">
      <p>Verify the LLM provider is reachable from the server.</p>

      {isGemini && (
        <div className="space-y-2">
          <p className="text-xs">A Gemini API key is required. Save it first, then test.</p>
          <div className="flex gap-2">
            <input
              className={inputClass}
              type="password"
              placeholder="AIza..."
              value={geminiKey}
              onChange={(e) => setGeminiKey(e.target.value)}
            />
            <Button
              variant="outline"
              loading={saveGemini.isPending}
              onClick={() => {
                if (!geminiKey) return
                saveGemini.mutate({ provider: 'gemini', gemini_api_key: geminiKey })
              }}
            >
              Save key
            </Button>
          </div>
          {saveError && (
            <p className="text-xs text-rose-500 flex items-center gap-1">
              <AlertCircle className="h-3 w-3" /> {saveError}
            </p>
          )}
        </div>
      )}

      <Button
        variant="outline"
        loading={testMutation.isPending}
        onClick={() => testMutation.mutate()}
      >
        Test LLM connectivity
      </Button>

      {result && (
        <div className={cn(
          'text-xs flex items-center gap-2',
          result.status === 'ok' ? 'text-emerald-500' : 'text-rose-500'
        )}>
          {result.status === 'ok' ? (
            <CheckCircle2 className="h-3 w-3" />
          ) : (
            <AlertCircle className="h-3 w-3" />
          )}
          <span>{result.detail}</span>
        </div>
      )}
    </div>
  )
}

// ── Final CTA ──────────────────────────────────────────────────────────────────

function SetupCompleteCard() {
  return (
    <motion.div
      initial={{ opacity: 0, y: 20 }}
      animate={{ opacity: 1, y: 0 }}
      className="mt-8"
    >
      <Card className="p-5 border-emerald-500/30 bg-emerald-500/5">
        <div className="flex items-center gap-3">
          <div className="h-10 w-10 rounded-[8px] bg-emerald-500/15 flex items-center justify-center">
            <CheckCircle2 className="h-5 w-5 text-emerald-500" />
          </div>
          <div>
            <p className="font-medium text-emerald-500">Setup complete!</p>
            <p className="text-xs text-[#8c919b]">
              Code Warden is ready. Install the app on your repositories to start reviewing PRs.
            </p>
          </div>
        </div>
      </Card>
    </motion.div>
  )
}