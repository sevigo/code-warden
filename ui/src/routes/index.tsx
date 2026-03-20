import { useQuery, useQueries } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { motion } from 'framer-motion'
import {
  Shield,
  Layers,
  Hash,
  Activity,
  RefreshCw,
  CheckCircle2,
  ArrowRight,
  Loader2,
  MessageSquare,
  Terminal,
} from 'lucide-react'
import StatusBadge from '@/components/StatusBadge'
import { api } from '@/lib/api'
import type { Repository, ScanState } from '@/lib/api'

const stagger = {
  hidden: {},
  show: { transition: { staggerChildren: 0.06 } },
}

const fadeUp = {
  hidden: { opacity: 0, y: 12 },
  show: { opacity: 1, y: 0, transition: { duration: 0.35 } },
}

function SummaryTile({ icon: Icon, label, value, accent }: {
  icon: React.ElementType; label: string; value: string; accent: string
}) {
  return (
    <motion.div variants={fadeUp} className="rounded-2xl bg-card p-5 flex flex-col gap-3">
      <div className={`h-9 w-9 rounded-xl flex items-center justify-center ${accent}`}>
        <Icon className="h-4.5 w-4.5" />
      </div>
      <div>
        <p className="text-2xl font-bold text-foreground font-mono">{value}</p>
        <p className="text-xs text-muted-foreground mt-0.5">{label}</p>
      </div>
    </motion.div>
  )
}

function ActiveScanCard({ repo, scanState }: { repo: Repository; scanState: ScanState }) {
  const progress = scanState.progress
  const pct = progress && progress.files_total > 0
    ? Math.round((progress.files_done / progress.files_total) * 100)
    : 0

  return (
    <motion.div variants={fadeUp}>
      <Link
        to={`/repos/${repo.id}`}
        className="block rounded-2xl bg-blue-500/5 p-5 hover:bg-blue-500/8 transition-colors group"
      >
        <div className="flex items-center justify-between mb-3">
          <div className="flex items-center gap-2.5">
            <div className="h-8 w-8 rounded-lg bg-blue-500/15 flex items-center justify-center">
              <RefreshCw className="h-4 w-4 text-blue-400 animate-spin" />
            </div>
            <div>
              <p className="text-sm font-medium text-foreground">{repo.full_name}</p>
              <p className="text-xs text-muted-foreground">{progress?.stage || 'Scanning...'}</p>
            </div>
          </div>
          <ArrowRight className="h-4 w-4 text-muted-foreground group-hover:text-foreground transition-colors" />
        </div>
        {/* progress bar */}
        <div className="h-1.5 rounded-full bg-blue-500/10 overflow-hidden">
          <div
            className="h-full rounded-full bg-blue-400 transition-all duration-500"
            style={{ width: pct > 0 ? `${pct}%` : '15%' }}
          />
        </div>
        {progress && progress.files_total > 0 && (
          <p className="text-xs text-blue-400/70 mt-2">
            {progress.files_done.toLocaleString()} / {progress.files_total.toLocaleString()} files
            {pct > 0 && <span className="ml-2 font-medium text-blue-400">{pct}%</span>}
          </p>
        )}
      </Link>
    </motion.div>
  )
}

function RepoRow({ repo }: { repo: Repository }) {
  const { data: scanState } = useQuery<ScanState | null>({
    queryKey: ['scanState', repo.id],
    queryFn: () => api.repos.status(repo.id),
    refetchInterval: (query) => {
      const s = query.state.data?.status
      return s === 'scanning' || s === 'in_progress' || s === 'pending' ? 2000 : false
    },
  })

  const isCompleted = scanState?.status === 'completed'

  return (
    <motion.div variants={fadeUp}>
      <Link
        to={`/repos/${repo.id}`}
        className="flex items-center gap-4 px-5 py-4 rounded-xl hover:bg-accent/40 transition-colors group"
      >
        <div className="flex-1 min-w-0">
          <p className="text-sm font-medium text-foreground truncate">{repo.full_name}</p>
          <p className="text-xs text-muted-foreground/60 font-mono truncate mt-0.5">{repo.clone_path}</p>
        </div>
        <StatusBadge status={scanState?.status} size="sm" />
        {isCompleted && (
          <Link
            to={`/repos/${repo.id}/chat`}
            onClick={(e) => e.stopPropagation()}
            className="p-2 rounded-lg opacity-0 group-hover:opacity-100 hover:bg-primary/10 text-muted-foreground hover:text-primary transition-all"
            title="Chat"
          >
            <MessageSquare className="h-4 w-4" />
          </Link>
        )}
        <ArrowRight className="h-4 w-4 text-muted-foreground/30 group-hover:text-muted-foreground transition-colors" />
      </Link>
    </motion.div>
  )
}

export default function Dashboard() {
  const { data: repos, isLoading, isError } = useQuery<Repository[]>({
    queryKey: ['repos'],
    queryFn: api.repos.list,
  })

  // Gather scan states for summary stats using useQueries (correct way for dynamic lists)
  const scanResults = useQueries({
    queries: (repos || []).map(repo => ({
      queryKey: ['scanState', repo.id],
      queryFn: () => api.repos.status(repo.id),
      refetchInterval: (query: any) => {
        const s = query.state.data?.status
        return s === 'scanning' || s === 'in_progress' || s === 'pending' ? 2000 : false
      },
    }))
  })

  const scanQueries = (repos || []).map((repo, i) => ({
    repo,
    ...scanResults[i]
  }))

  const readyCount = scanQueries.filter(q => q.data?.status === 'completed').length
  const activeScans = scanQueries.filter(q =>
    q.data?.status === 'scanning' || q.data?.status === 'in_progress' || q.data?.status === 'pending'
  )
  const totalRepos = repos?.length ?? 0
  const totalChunks = scanQueries.reduce((acc, q) => acc + (q.data?.artifacts?.chunks_count ?? 0), 0)

  if (isLoading) {
    return (
      <div className="flex flex-col items-center justify-center py-32 gap-3">
        <Loader2 className="h-6 w-6 animate-spin text-primary" />
        <p className="text-sm text-muted-foreground">Loading workspace...</p>
      </div>
    )
  }

  if (isError) {
    return (
      <div className="flex flex-col items-center justify-center py-32 text-center gap-4">
        <div className="h-12 w-12 rounded-2xl bg-red-500/10 flex items-center justify-center">
          <Shield className="h-6 w-6 text-red-400" />
        </div>
        <p className="text-sm text-muted-foreground">Failed to load repositories.</p>
      </div>
    )
  }

  // Empty state
  if (!repos || repos.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-24 text-center animate-fade-in">
        <div className="relative mb-6">
          <div className="absolute inset-0 rounded-3xl bg-primary/15 blur-2xl scale-150" />
          <div className="relative h-20 w-20 rounded-3xl bg-primary/10 flex items-center justify-center">
            <Shield className="h-10 w-10 text-primary" />
          </div>
        </div>
        <h1 className="text-2xl font-bold text-foreground mb-2">Welcome to Code Warden</h1>
        <p className="text-muted-foreground text-sm max-w-md mb-10">
          Add a local repository to get started with AI-powered code intelligence.
        </p>

        {/* Getting started */}
        <div className="w-full max-w-sm space-y-6 text-left">
          <div className="space-y-3">
            {[
              { step: '1', text: 'Click the + button in the sidebar to add a repository' },
              { step: '2', text: 'Run the initial scan to index the codebase' },
              { step: '3', text: 'Start chatting with your code' },
            ].map(({ step, text }) => (
              <div key={step} className="flex items-start gap-3">
                <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary text-primary-foreground text-xs font-bold">
                  {step}
                </span>
                <span className="text-sm text-muted-foreground pt-0.5">{text}</span>
              </div>
            ))}
          </div>

          {/* CLI hint */}
          <div className="rounded-xl bg-card p-4">
            <div className="flex items-center gap-2 text-xs text-muted-foreground mb-2">
              <Terminal className="h-3.5 w-3.5" />
              Or use the CLI
            </div>
            <code className="text-xs text-foreground font-mono block bg-accent/30 rounded-lg px-3 py-2">
              warden-cli prescan --path /your/repo
            </code>
          </div>
        </div>
      </div>
    )
  }

  return (
    <motion.div
      className="space-y-8"
      initial="hidden"
      animate="show"
      variants={stagger}
    >
      {/* Header */}
      <motion.div variants={fadeUp}>
        <h1 className="text-2xl font-bold text-foreground">Overview</h1>
        <p className="text-sm text-muted-foreground mt-1">Your code intelligence workspace</p>
      </motion.div>

      {/* Summary tiles */}
      <motion.div variants={stagger} className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <SummaryTile icon={Layers} label="Repositories" value={totalRepos.toLocaleString()} accent="bg-violet-500/10 text-violet-400" />
        <SummaryTile icon={CheckCircle2} label="Indexed" value={readyCount.toLocaleString()} accent="bg-emerald-500/10 text-emerald-400" />
        <SummaryTile icon={Activity} label="Active scans" value={activeScans.length.toString()} accent="bg-blue-500/10 text-blue-400" />
        <SummaryTile icon={Hash} label="Total chunks" value={totalChunks.toLocaleString()} accent="bg-amber-500/10 text-amber-400" />
      </motion.div>

      {/* Active scans spotlight */}
      {activeScans.length > 0 && (
        <motion.div variants={fadeUp} className="space-y-3">
          <h2 className="text-sm font-semibold text-muted-foreground uppercase tracking-wider">Active Scans</h2>
          <div className="space-y-2">
            {activeScans.map(({ repo, data }) => (
              <ActiveScanCard key={repo.id} repo={repo} scanState={data!} />
            ))}
          </div>
        </motion.div>
      )}

      {/* Repository list */}
      <motion.div variants={fadeUp} className="space-y-3">
        <h2 className="text-sm font-semibold text-muted-foreground uppercase tracking-wider">All Repositories</h2>
        <motion.div variants={stagger} className="divide-y divide-border/30">
          {repos.map((repo) => (
            <RepoRow key={repo.id} repo={repo} />
          ))}
        </motion.div>
      </motion.div>
    </motion.div>
  )
}
