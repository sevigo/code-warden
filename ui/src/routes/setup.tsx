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
  Zap,
  FileCode,
} from 'lucide-react'
import { api } from '@/lib/api'
import type { SetupStatus } from '@/lib/api'
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

// ── Service Pill Component ─────────────────────────────────────────────────

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

// ── Code Block Component ─────────────────────────────────────────────────────

function CodeBlock({ children, title }: { children: string; title?: string }) {
  return (
    <div className="mt-3 rounded-[6px] bg-[#0d0e12] border border-[#2d2f36] overflow-hidden">
      {title && (
        <div className="px-3 py-2 border-b border-[#2d2f36] bg-[#15181e] text-xs text-[#8c919b] flex items-center gap-2">
          <FileCode className="h-3 w-3" />
          {title}
        </div>
      )}
      <pre className="p-3 text-xs font-mono text-[#efeff1] overflow-x-auto">
        <code>{children}</code>
      </pre>
    </div>
  )
}

// ── Main Page Component ──────────────────────────────────────────────────────

export default function SetupPage() {
  const { data: status } = useQuery<SetupStatus>({
    queryKey: ['setup-status'],
    queryFn: api.setup.status,
    refetchInterval: 10_000,
  })

  const githubConfigured = status?.github_app.configured ?? false
  const dbOk = status?.services.database.status === 'ok'
  const qdrantOk = status?.services.qdrant.status === 'ok'
  const servicesOk = dbOk && qdrantOk
  const allDone = githubConfigured && servicesOk

  // Determine active step
  const getActiveStep = (): 1 | 2 | 3 | 4 | 5 | 6 => {
    if (!githubConfigured) return 1
    return 2
  }

  const activeStep = getActiveStep()

  return (
    <div className="max-w-2xl mx-auto py-10 px-4">
      {/* Header */}
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

      {/* Service Status */}
      <Card className="mb-8 p-4">
        <div className="flex items-center gap-2 flex-wrap">
          <div className="flex items-center gap-2 text-sm">
            <Database className="h-4 w-4 text-[#8c919b]" />
            <ServicePill ok={dbOk} label="PostgreSQL" />
          </div>
          
          <div className="w-px h-4 bg-[#e1e3e6] dark:bg-[#2d2f36] mx-1" />
          
          <div className="flex items-center gap-2 text-sm">
            <Server className="h-4 w-4 text-[#8c919b]" />
            <ServicePill ok={qdrantOk} label="Qdrant" />
          </div>
          
          {status && (
            <span className={cn(
              "text-xs ml-auto font-medium",
              allDone ? 'text-emerald-500' : 'text-amber-500'
            )}>
              {allDone ? '✓ All systems go' : `${activeStep} of 6 steps complete`}
            </span>
          )}
        </div>
      </Card>

      {/* Steps */}
      <motion.div variants={stagger} initial="hidden" animate="show" className="space-y-0">
        {/* Step 1 */}
        <Step 
          num={1} 
          title="Create the GitHub App" 
          done={githubConfigured}
          active={activeStep === 1}
        >
          <p>
            Go to <strong className="text-foreground">GitHub → Settings → Developer settings → GitHub Apps</strong>
          </p>
          <ul className="list-disc list-inside space-y-0.5 mt-2">
            <li>Set Webhook URL to your server endpoint</li>
            <li>Grant <em>Contents, Issues, Metadata, Pull requests</em> read/write</li>
            <li>Subscribe to: <em>Issue comment, Issues, Pull request, Push</em></li>
            <li>Generate a private key and save it to <code className="font-mono text-xs bg-[#f1f2f3] dark:bg-[#1e2025] px-1 rounded">keys/</code></li>
          </ul>
          <a
            href="https://github.com/settings/apps/new"
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1.5 mt-3 text-[#2264d6] hover:underline text-xs"
          >
            <Github className="h-3.5 w-3.5" /> 
            Open GitHub App creation 
            <ExternalLink className="h-3 w-3" />
          </a>
        </Step>

        {/* Step 2 */}
        <Step 
          num={2} 
          title="Configure config.yaml" 
          done={githubConfigured}
          active={activeStep === 2}
        >
          <p>Edit <code className="font-mono text-xs bg-[#f1f2f3] dark:bg-[#1e2025] px-1 rounded">config.yaml</code>:</p>
          <CodeBlock title="config.yaml">
{`github:
  app_id: 12345
  webhook_secret: "your-secret"
  private_key_path: "keys/app.pem"

ai:
  llm_provider: "ollama"
  generator_model: "qwen2.5-coder:7b"
  embedder_model: "nomic-embed-text"`}
          </CodeBlock>
        </Step>

        {/* Step 3 */}
        <Step 
          num={3} 
          title="Start infrastructure" 
          done={servicesOk}
          active={activeStep === 3}
        >
          <p>Start required services with Docker:</p>
          <CodeBlock>
            docker-compose up -d
          </CodeBlock>
        </Step>

        {/* Step 4 */}
        <Step 
          num={4} 
          title="Install the GitHub App on your repos" 
          done={githubConfigured && !!status?.github_app.app_name}
          active={activeStep === 4}
        >
          {status?.github_app.install_url ? (
            <a
              href={status.github_app.install_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1.5 text-[#2264d6] hover:underline text-sm"
            >
              <Github className="h-4 w-4" /> 
              Install {status.github_app.app_name}
              <ExternalLink className="h-3 w-3" />
            </a>
          ) : (
            <p className="text-[#8c919b]">
              Once your app is configured, its install URL will appear here.
            </p>
          )}
        </Step>

        {/* Step 5 */}
        <Step 
          num={5} 
          title="Index a repository" 
          done={false}
          active={activeStep === 5}
        >
          <p>Add a repository from the dashboard and trigger a scan:</p>
          <CodeBlock>
            ./bin/warden-cli prescan /path/to/repo
          </CodeBlock>
        </Step>

        {/* Step 6 */}
        <Step 
          num={6} 
          title="Trigger your first review" 
          done={false}
          active={activeStep === 6}
        >
          <p>Open a pull request and comment:</p>
          <div className="mt-2 inline-flex items-center gap-2 px-3 py-1.5 rounded-[6px] bg-[#2264d6]/10 text-[#2264d6] font-mono text-sm">
            <Zap className="h-3.5 w-3.5" />
            /review
          </div>
        </Step>
      </motion.div>

      {/* CTA */}
      {allDone && (
        <motion.div 
          initial={{ opacity: 0, y: 20 }}
          animate={{ opacity: 1, y: 0 }}
          className="mt-8"
        >
          <Card className="p-5 border-emerald-500/30 bg-emerald-500/5">
            <div className="flex items-center justify-between gap-4">
              <div className="flex items-center gap-3">
                <div className="h-10 w-10 rounded-[8px] bg-emerald-500/15 flex items-center justify-center">
                  <CheckCircle2 className="h-5 w-5 text-emerald-500" />
                </div>
                <div>
                  <p className="font-medium text-emerald-500">Setup complete!</p>
                  <p className="text-xs text-[#8c919b]">
                    You're ready to add repositories and start reviewing PRs
                  </p>
                </div>
              </div>
              
              <Button asChild>
                <Link to="/" className="flex items-center gap-1.5">
                  Go to Dashboard
                  <ArrowRight className="h-4 w-4" />
                </Link>
              </Button>
            </div>
          </Card>
        </motion.div>
      )}
    </div>
  )
}
