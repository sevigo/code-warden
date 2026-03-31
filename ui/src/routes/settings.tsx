import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { motion } from 'framer-motion'
import {
  Settings,
  CheckCircle2,
  XCircle,
  Loader2,
  Database,
  Server,
  Github,
  Cpu,
  ExternalLink,
} from 'lucide-react'
import { api } from '@/lib/api'
import type { AppConfig, SetupStatus } from '@/lib/api'

const stagger = { hidden: {}, show: { transition: { staggerChildren: 0.05 } } }
const fadeUp  = { hidden: { opacity: 0, y: 8 }, show: { opacity: 1, y: 0, transition: { duration: 0.28 } } }

// ── Helpers ──────────────────────────────────────────────────────────────────

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between py-3 border-b border-border/20 last:border-0 gap-4">
      <span className="text-sm text-muted-foreground shrink-0">{label}</span>
      <span className="text-sm text-foreground text-right">{value}</span>
    </div>
  )
}

function MonoValue({ v }: { v: string }) {
  return <code className="font-mono text-xs bg-accent/50 px-2 py-0.5 rounded text-foreground">{v || '—'}</code>
}

function StatusPill({ ok, label }: { ok: boolean; label: string }) {
  return ok ? (
    <span className="inline-flex items-center gap-1 text-xs font-medium text-emerald-400 bg-emerald-500/10 px-2 py-0.5 rounded-lg">
      <CheckCircle2 className="h-3 w-3" /> {label}
    </span>
  ) : (
    <span className="inline-flex items-center gap-1 text-xs font-medium text-red-400 bg-red-500/10 px-2 py-0.5 rounded-lg">
      <XCircle className="h-3 w-3" /> {label}
    </span>
  )
}

function ProviderBadge({ provider }: { provider: string }) {
  const style = provider === 'gemini'
    ? 'bg-blue-500/15 text-blue-400'
    : 'bg-violet-500/15 text-violet-400'
  return (
    <span className={`text-xs font-semibold px-2 py-0.5 rounded capitalize ${style}`}>
      {provider}
    </span>
  )
}

// ── Service Check ─────────────────────────────────────────────────────────────

function ServiceCheck({
  icon: Icon,
  name,
  service,
}: {
  icon: React.ElementType
  name: string
  service: { status: string; latency_ms: number } | undefined
}) {
  const ok = service?.status === 'ok'
  return (
    <div className="flex items-center gap-3 py-3 border-b border-border/20 last:border-0">
      <div className={`h-8 w-8 rounded-xl flex items-center justify-center shrink-0 ${ok ? 'bg-emerald-500/10' : 'bg-red-500/10'}`}>
        <Icon className={`h-4 w-4 ${ok ? 'text-emerald-400' : 'text-red-400'}`} />
      </div>
      <div className="flex-1">
        <p className="text-sm text-foreground">{name}</p>
        {service && (
          <p className="text-xs text-muted-foreground">{service.latency_ms}ms response time</p>
        )}
      </div>
      {service ? (
        <StatusPill ok={ok} label={ok ? 'Online' : 'Offline'} />
      ) : (
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      )}
    </div>
  )
}

// ── Section ───────────────────────────────────────────────────────────────────

function Section({ icon: Icon, title, children }: {
  icon: React.ElementType
  title: string
  children: React.ReactNode
}) {
  return (
    <motion.div variants={fadeUp} className="rounded-2xl bg-card overflow-hidden">
      <div className="flex items-center gap-2.5 px-5 py-4 border-b border-border/20">
        <div className="h-7 w-7 rounded-lg bg-accent/50 flex items-center justify-center shrink-0">
          <Icon className="h-3.5 w-3.5 text-muted-foreground" />
        </div>
        <h2 className="text-sm font-semibold text-foreground">{title}</h2>
      </div>
      <div className="px-5">{children}</div>
    </motion.div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

export default function SettingsPage() {
  const { data: cfg, isLoading: cfgLoading } = useQuery<AppConfig>({
    queryKey: ['config'],
    queryFn: api.config.get,
  })

  const { data: status, isLoading: statusLoading } = useQuery<SetupStatus>({
    queryKey: ['setup-status'],
    queryFn: api.setup.status,
    retry: false,
  })

  const isLoading = cfgLoading || statusLoading

  return (
    <motion.div className="space-y-6" initial="hidden" animate="show" variants={stagger}>
      {/* Header */}
      <motion.div variants={fadeUp} className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-foreground">Settings</h1>
          <p className="text-sm text-muted-foreground mt-0.5">Current configuration — read-only</p>
        </div>
        <div className="flex items-center gap-2">
          <Settings className="h-4 w-4 text-muted-foreground" />
        </div>
      </motion.div>

      {isLoading ? (
        <div className="flex items-center justify-center py-20 gap-2 text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          <span className="text-sm">Loading configuration...</span>
        </div>
      ) : (
        <>
          {/* AI Configuration */}
          <Section icon={Cpu} title="AI Configuration">
            <Row label="Provider" value={cfg ? <ProviderBadge provider={cfg.ai.llm_provider} /> : '—'} />
            <Row label="Generator model" value={<MonoValue v={cfg?.ai.generator_model ?? ''} />} />
            <Row label="Embedder model"  value={<MonoValue v={cfg?.ai.embedder_model ?? ''} />} />
          </Section>

          {/* GitHub App */}
          <Section icon={Github} title="GitHub App">
            <Row
              label="App ID"
              value={cfg?.github.app_id ? (
                <MonoValue v={String(cfg.github.app_id)} />
              ) : (
                <span className="text-muted-foreground/50 text-sm">Not configured</span>
              )}
            />
            <Row
              label="Webhook"
              value={<StatusPill ok={cfg?.github.webhook_configured ?? false} label={cfg?.github.webhook_configured ? 'Configured' : 'Not configured'} />}
            />
            <div className="py-3">
              <Link
                to="/setup"
                className="inline-flex items-center gap-1.5 text-xs text-primary hover:underline"
              >
                <ExternalLink className="h-3 w-3" />
                Re-run setup wizard
              </Link>
            </div>
          </Section>

          {/* Storage */}
          <Section icon={Database} title="Storage">
            <Row label="Qdrant host" value={<MonoValue v={cfg?.storage.qdrant_host ?? ''} />} />
          </Section>

          {/* Service Health */}
          <Section icon={Server} title="Service Health">
            <ServiceCheck
              icon={Database}
              name="PostgreSQL Database"
              service={status?.services.database}
            />
            <ServiceCheck
              icon={Server}
              name="Qdrant Vector Store"
              service={status?.services.qdrant}
            />
          </Section>

          {/* Config file hint */}
          <motion.div variants={fadeUp} className="rounded-2xl bg-card/50 border border-border/30 px-5 py-4">
            <p className="text-xs text-muted-foreground">
              Configuration is loaded from <code className="font-mono bg-accent/50 px-1.5 py-0.5 rounded">config.yaml</code> in the working directory.
              Environment variables override file settings using the format{' '}
              <code className="font-mono bg-accent/50 px-1.5 py-0.5 rounded">SECTION_KEY</code>{' '}
              (e.g. <code className="font-mono bg-accent/50 px-1.5 py-0.5 rounded">AI_GENERATOR_MODEL</code>).
            </p>
          </motion.div>
        </>
      )}
    </motion.div>
  )
}
