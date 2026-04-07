import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { motion } from 'framer-motion'
import {
  CheckCircle2,
  XCircle,
  Loader2,
  Database,
  Server,
  Github,
  Cpu,
  ExternalLink,
  Check,
  Zap,
  Copy,
} from 'lucide-react'
import { api } from '@/lib/api'
import type { AppConfig, SetupStatus } from '@/lib/api'
import { Card } from '@/components/ui/card'
import { cn } from '@/lib/utils'

const stagger = { hidden: {}, show: { transition: { staggerChildren: 0.05 } } }
const fadeUp = { hidden: { opacity: 0, y: 8 }, show: { opacity: 1, y: 0, transition: { duration: 0.28 } } }

// ── Helper Components ─────────────────────────────────────────────────────────

function ConfigRow({ label, value, status }: { label: string; value: React.ReactNode; status?: 'ok' | 'warning' | 'error' }) {
  return (
    <div className="flex items-center justify-between py-3 border-b border-[#e1e3e6] dark:border-[#2d2f36] last:border-0 gap-4">
      <span className="text-sm text-[#8c919b] shrink-0">{label}</span>
      <div className="flex items-center gap-2">
        {status && (
          <div className={cn(
            "h-2 w-2 rounded-full",
            status === 'ok' && "bg-emerald-500",
            status === 'warning' && "bg-amber-500",
            status === 'error' && "bg-rose-500"
          )} />
        )}
        <span className="text-sm text-foreground text-right">{value}</span>
      </div>
    </div>
  )
}

function MonoValue({ v, copyable = false }: { v: string; copyable?: boolean }) {
  const [copied, setCopied] = useState(false)
  
  const handleCopy = () => {
    navigator.clipboard.writeText(v)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }
  
  return (
    <div className="flex items-center gap-2">
      <code className="font-mono text-xs bg-[#f1f2f3] dark:bg-[#1e2025] px-2 py-1 rounded text-foreground">
        {v || '—'}
      </code>
      {copyable && v && (
        <button
          onClick={handleCopy}
          className="p-1 rounded-[4px] hover:bg-[#f1f2f3] dark:hover:bg-[#2d2f36] text-[#8c919b] transition-colors"
          title="Copy"
        >
          {copied ? <Check className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3" />}
        </button>
      )}
    </div>
  )
}

function StatusPill({ ok, label }: { ok: boolean; label: string }) {
  return ok ? (
    <span className="inline-flex items-center gap-1 text-xs font-medium text-emerald-500 bg-emerald-500/10 px-2 py-1 rounded-[5px]">
      <CheckCircle2 className="h-3 w-3" /> {label}
    </span>
  ) : (
    <span className="inline-flex items-center gap-1 text-xs font-medium text-rose-500 bg-rose-500/10 px-2 py-1 rounded-[5px]">
      <XCircle className="h-3 w-3" /> {label}
    </span>
  )
}

function ProviderBadge({ provider }: { provider: string }) {
  const isGemini = provider === 'gemini'
  return (
    <span className={cn(
      "text-xs font-semibold px-2 py-1 rounded-[4px] capitalize",
      isGemini 
        ? 'bg-blue-500/10 text-blue-500' 
        : 'bg-violet-500/10 text-violet-500'
    )}>
      {provider}
    </span>
  )
}

function ServiceCard({
  icon: Icon,
  name,
  service,
  description,
}: {
  icon: React.ElementType
  name: string
  service: { status: string; latency_ms: number } | undefined
  description?: string
}) {
  const ok = service?.status === 'ok'
  
  return (
    <Card className="p-4">
      <div className="flex items-start gap-3">
        <div className={cn(
          "h-9 w-9 rounded-[6px] flex items-center justify-center shrink-0",
          ok ? 'bg-emerald-500/10' : 'bg-rose-500/10'
        )}>
          <Icon className={cn("h-4 w-4", ok ? 'text-emerald-500' : 'text-rose-500')} />
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center justify-between">
            <p className="text-sm font-medium text-foreground">{name}</p>
            {service ? (
              <StatusPill ok={ok} label={ok ? 'Online' : 'Offline'} />
            ) : (
              <Loader2 className="h-3 w-3 animate-spin text-[#8c919b]" />
            )}
          </div>
          {description && (
            <p className="text-xs text-[#8c919b] mt-0.5">{description}</p>
          )}
          {service && (
            <p className="text-xs text-[#8c919b] mt-1">{service.latency_ms}ms response</p>
          )}
        </div>
      </div>
    </Card>
  )
}

// ── Section Component ─────────────────────────────────────────────────────────

function Section({ icon: Icon, title, children, action }: {
  icon: React.ElementType
  title: string
  children: React.ReactNode
  action?: React.ReactNode
}) {
  return (
    <motion.div variants={fadeUp}>
      <Card className="overflow-hidden">
        <div className="flex items-center justify-between px-5 py-4 border-b border-[#e1e3e6] dark:border-[#2d2f36]">
          <div className="flex items-center gap-2.5">
            <div className="h-7 w-7 rounded-[6px] bg-[#f1f2f3] dark:bg-[#1e2025] flex items-center justify-center shrink-0">
              <Icon className="h-3.5 w-3.5 text-[#8c919b]" />
            </div>
            <h2 className="text-sm font-semibold text-foreground">{title}</h2>
          </div>
          {action}
        </div>
        <div className="px-5">{children}</div>
      </Card>
    </motion.div>
  )
}

// ── Main Page Component ───────────────────────────────────────────────────────

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
          <p className="text-sm text-[#8c919b] mt-0.5">Configuration and system status</p>
        </div>
      </motion.div>

      {isLoading ? (
        <div className="flex items-center justify-center py-20 gap-2 text-[#8c919b]">
          <Loader2 className="h-4 w-4 animate-spin" />
          <span className="text-sm">Loading configuration...</span>
        </div>
      ) : (
        <div className="space-y-6">
          {/* Service Health */}
          <Section icon={Server} title="Service Health">
            <div className="py-4 grid grid-cols-1 sm:grid-cols-2 gap-3">
              <ServiceCard
                icon={Database}
                name="PostgreSQL Database"
                service={status?.services.database}
                description="Stores repository metadata and job history"
              />
              <ServiceCard
                icon={Server}
                name="Qdrant Vector Store"
                service={status?.services.qdrant}
                description="Manages code embeddings and vector search"
              />
            </div>
          </Section>

          {/* AI Configuration */}
          <Section icon={Cpu} title="AI Configuration">
            <div className="py-2">
              <ConfigRow 
                label="Provider" 
                value={cfg ? <ProviderBadge provider={cfg.ai.llm_provider} /> : '—'} 
              />
              <ConfigRow 
                label="Generator Model" 
                value={<MonoValue v={cfg?.ai.generator_model ?? ''} />} 
              />
              <ConfigRow 
                label="Embedder Model" 
                value={<MonoValue v={cfg?.ai.embedder_model ?? ''} />} 
              />
              <ConfigRow
                label="Consensus Models"
                value={(() => {
                  const ai = cfg?.ai as { comparison_models?: string[] } | undefined
                  return (
                    <span className="text-xs text-[#8c919b]">
                      {ai?.comparison_models?.length
                        ? `${ai.comparison_models.length} models configured`
                        : 'Not configured'}
                    </span>
                  )
                })()}
              />
            </div>
          </Section>

          {/* GitHub App */}
          <Section 
            icon={Github} 
            title="GitHub App"
            action={
              <Link
                to="/setup"
                className="text-xs text-[#2264d6] hover:underline dark:text-[#2b89ff] flex items-center gap-1"
              >
                <ExternalLink className="h-3 w-3" />
                Setup Wizard
              </Link>
            }
          >
            <div className="py-2">
              <ConfigRow 
                label="App ID" 
                value={cfg?.github.app_id ? <MonoValue v={String(cfg.github.app_id)} /> : <span className="text-[#8c919b]">Not configured</span>} 
                status={cfg?.github.app_id ? 'ok' : 'warning'}
              />
              <ConfigRow 
                label="Webhook" 
                value={<StatusPill ok={cfg?.github.webhook_configured ?? false} label={cfg?.github.webhook_configured ? 'Configured' : 'Not configured'} />}
                status={cfg?.github.webhook_configured ? 'ok' : 'warning'}
              />
              <ConfigRow
                label="Private Key"
                value={(() => {
                  const github = cfg?.github as { private_key_path?: string } | undefined
                  return <StatusPill ok={!!github?.private_key_path} label={github?.private_key_path ? 'Configured' : 'Not configured'} />
                })()}
                status={(() => {
                  const github = cfg?.github as { private_key_path?: string } | undefined
                  return github?.private_key_path ? 'ok' : 'warning'
                })()}
              />
            </div>
          </Section>

          {/* Storage */}
          <Section icon={Database} title="Storage Configuration">
            <div className="py-2">
              <ConfigRow 
                label="Qdrant Host" 
                value={<MonoValue v={cfg?.storage.qdrant_host ?? ''} copyable />} 
              />
              <ConfigRow
                label="Collection Prefix"
                value={(() => {
                  const storage = cfg?.storage as { collection_prefix?: string } | undefined
                  return <MonoValue v={storage?.collection_prefix ?? 'default'} />
                })()}
              />
            </div>
          </Section>

          {/* Config File Info */}
          <motion.div variants={fadeUp} className="bg-[#f1f2f3]/50 dark:bg-[#1e2025]/50 rounded-[8px] border border-[#e1e3e6] dark:border-[#2d2f36] px-5 py-4">
            <div className="flex items-start gap-3">
              <div className="h-8 w-8 rounded-[6px] bg-[#2264d6]/10 flex items-center justify-center shrink-0 mt-0.5">
                <Zap className="h-4 w-4 text-[#2264d6]" />
              </div>
              <div>
                <h3 className="text-sm font-medium text-foreground mb-1">Configuration Management</h3>
                <p className="text-xs text-[#8c919b] leading-relaxed">
                  Configuration is loaded from <code className="font-mono bg-white dark:bg-[#15181e] px-1.5 py-0.5 rounded text-[10px]">config.yaml</code> in the working directory. 
                  Environment variables override file settings using the format{' '}
                  <code className="font-mono bg-white dark:bg-[#15181e] px-1.5 py-0.5 rounded text-[10px]">SECTION_KEY</code>
                  {' '}(e.g. <code className="font-mono bg-white dark:bg-[#15181e] px-1.5 py-0.5 rounded text-[10px]">AI_GENERATOR_MODEL</code>).
                </p>
              </div>
            </div>
          </motion.div>
        </div>
      )}
    </motion.div>
  )
}
