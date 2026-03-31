import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { motion } from 'framer-motion'
import {
  Shield,
  CheckCircle2,
  Circle,
  ExternalLink,
  Github,
  Server,
  Database,
  ArrowRight,
} from 'lucide-react'
import { api } from '@/lib/api'
import type { SetupStatus } from '@/lib/api'

const stagger = { hidden: {}, show: { transition: { staggerChildren: 0.06 } } }
const fadeUp  = { hidden: { opacity: 0, y: 8 }, show: { opacity: 1, y: 0, transition: { duration: 0.28 } } }

// ── Step ──────────────────────────────────────────────────────────────────────

function Step({
  num,
  title,
  done,
  children,
}: {
  num: number
  title: string
  done: boolean
  children: React.ReactNode
}) {
  return (
    <motion.div variants={fadeUp} className="flex gap-4">
      <div className="flex flex-col items-center">
        <div className={`
          h-8 w-8 rounded-full flex items-center justify-center text-sm font-bold shrink-0
          ${done ? 'bg-emerald-500/15 text-emerald-400' : 'bg-accent text-muted-foreground'}
        `}>
          {done ? <CheckCircle2 className="h-4 w-4" /> : num}
        </div>
        <div className="w-px flex-1 mt-2 bg-border/30" />
      </div>
      <div className="pb-8 flex-1 min-w-0">
        <p className={`font-medium mb-2 ${done ? 'text-emerald-400' : 'text-foreground'}`}>{title}</p>
        <div className="text-sm text-muted-foreground space-y-1">{children}</div>
      </div>
    </motion.div>
  )
}

// ── Service Pill ──────────────────────────────────────────────────────────────

function ServicePill({ ok, label }: { ok: boolean | undefined; label: string }) {
  if (ok === undefined) return (
    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground bg-accent/40 px-2.5 py-1 rounded-lg">
      <Circle className="h-3 w-3" /> {label}
    </span>
  )
  return ok ? (
    <span className="inline-flex items-center gap-1.5 text-xs text-emerald-400 bg-emerald-500/10 px-2.5 py-1 rounded-lg font-medium">
      <CheckCircle2 className="h-3 w-3" /> {label} OK
    </span>
  ) : (
    <span className="inline-flex items-center gap-1.5 text-xs text-red-400 bg-red-500/10 px-2.5 py-1 rounded-lg font-medium">
      <Circle className="h-3 w-3" /> {label} unreachable
    </span>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

export default function SetupPage() {
  const { data: status } = useQuery<SetupStatus>({
    queryKey: ['setup-status'],
    queryFn: api.setup.status,
    refetchInterval: 10_000,
  })

  const githubConfigured = status?.github_app.configured ?? false
  const dbOk  = status?.services.database.status === 'ok'
  const qdrantOk = status?.services.qdrant.status === 'ok'
  const servicesOk = dbOk && qdrantOk
  const allDone = githubConfigured && servicesOk

  return (
    <div className="max-w-2xl mx-auto py-10 px-4 animate-fade-in">
      {/* Header */}
      <div className="flex items-center gap-4 mb-10">
        <div className="h-12 w-12 rounded-2xl bg-primary/10 flex items-center justify-center shrink-0">
          <Shield className="h-6 w-6 text-primary" />
        </div>
        <div>
          <h1 className="text-2xl font-bold text-foreground">Setup Code Warden</h1>
          <p className="text-sm text-muted-foreground mt-0.5">Complete the steps below to start reviewing pull requests.</p>
        </div>
      </div>

      {/* Service status strip */}
      <div className="flex items-center gap-2 mb-8 flex-wrap">
        <ServicePill ok={dbOk} label="PostgreSQL" />
        <ServicePill ok={qdrantOk} label="Qdrant" />
        {status && (
          <span className="text-xs text-muted-foreground ml-auto">
            {allDone ? '✓ All systems go' : 'Some steps incomplete'}
          </span>
        )}
      </div>

      {/* Steps */}
      <motion.div variants={stagger} initial="hidden" animate="show">
        <Step num={1} title="Create the GitHub App" done={githubConfigured}>
          <p>Go to <strong className="text-foreground">GitHub → Settings → Developer settings → GitHub Apps → New GitHub App</strong>.</p>
          <ul className="list-disc list-inside space-y-0.5 mt-2">
            <li>Set Webhook URL to <code className="font-mono text-xs bg-accent/50 px-1 rounded">https://your-host/webhook</code></li>
            <li>Grant <em>Contents, Issues, Metadata, Pull requests</em> read/write</li>
            <li>Subscribe to: <em>Issue comment, Issues, Pull request, Push</em></li>
            <li>Generate a private key and save it to <code className="font-mono text-xs bg-accent/50 px-1 rounded">keys/</code></li>
          </ul>
          <a
            href="https://github.com/settings/apps/new"
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1.5 mt-3 text-primary hover:underline text-xs"
          >
            <Github className="h-3.5 w-3.5" /> Open GitHub App creation <ExternalLink className="h-3 w-3" />
          </a>
        </Step>

        <Step num={2} title="Configure config.yaml" done={githubConfigured}>
          <p>Edit <code className="font-mono text-xs bg-accent/50 px-1 rounded">config.yaml</code> with your GitHub App credentials:</p>
          <pre className="mt-2 bg-accent/30 rounded-lg p-3 text-xs font-mono overflow-x-auto whitespace-pre leading-relaxed">
{`github:
  app_id: 12345
  webhook_secret: "your-secret"
  private_key_path: "keys/app.pem"

ai:
  llm_provider: "ollama"
  generator_model: "qwen2.5-coder:7b"
  embedder_model: "nomic-embed-text"`}
          </pre>
        </Step>

        <Step num={3} title="Start infrastructure" done={servicesOk}>
          <div className="flex flex-wrap gap-2 mb-2">
            <div className="flex items-center gap-1.5 text-xs">
              <Database className="h-3.5 w-3.5" />
              <span>PostgreSQL</span>
              {status && <ServicePill ok={dbOk} label="" />}
            </div>
            <div className="flex items-center gap-1.5 text-xs">
              <Server className="h-3.5 w-3.5" />
              <span>Qdrant</span>
              {status && <ServicePill ok={qdrantOk} label="" />}
            </div>
          </div>
          <pre className="bg-accent/30 rounded-lg p-3 text-xs font-mono">
{`docker-compose up -d`}
          </pre>
        </Step>

        <Step num={4} title="Install the GitHub App on your repos" done={githubConfigured && status?.github_app.app_name !== ''}>
          {status?.github_app.install_url ? (
            <a
              href={status.github_app.install_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1.5 text-primary hover:underline text-xs"
            >
              <Github className="h-3.5 w-3.5" /> Install {status.github_app.app_name || 'GitHub App'} <ExternalLink className="h-3 w-3" />
            </a>
          ) : (
            <p>Once your app is configured, its install URL will appear here.</p>
          )}
        </Step>

        <Step num={5} title="Index a repository" done={false}>
          <p>Add a repository from the dashboard and trigger a scan, or use the CLI:</p>
          <pre className="mt-2 bg-accent/30 rounded-lg p-3 text-xs font-mono overflow-x-auto">
{`./bin/warden-cli prescan /path/to/repo`}
          </pre>
        </Step>

        <Step num={6} title="Trigger your first review" done={false}>
          <p>Open a pull request and comment:</p>
          <pre className="mt-2 bg-accent/30 rounded-lg p-3 text-xs font-mono">/review</pre>
        </Step>
      </motion.div>

      {/* CTA */}
      {allDone && (
        <div className="mt-4 p-4 rounded-xl bg-emerald-500/10 border border-emerald-500/20 flex items-center justify-between gap-4 animate-fade-in">
          <div>
            <p className="font-medium text-emerald-400 text-sm">Setup complete!</p>
            <p className="text-xs text-muted-foreground mt-0.5">You're ready to add repositories and start reviewing PRs.</p>
          </div>
          <Link
            to="/"
            className="flex items-center gap-1.5 text-sm font-medium text-emerald-400 hover:text-emerald-300 transition-colors shrink-0"
          >
            Go to Dashboard <ArrowRight className="h-4 w-4" />
          </Link>
        </div>
      )}
    </div>
  )
}
