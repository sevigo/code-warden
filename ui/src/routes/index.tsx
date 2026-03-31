import { useQuery, useQueries } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { motion } from 'framer-motion'
import {
  GitPullRequest,
  Layers,
  AlertTriangle,
  TrendingDown,
  CheckCircle2,
  XCircle,
  Loader2,
  Clock,
  MessageSquare,
  ChevronRight,
  Shield,
  Plus,
} from 'lucide-react'
import StatusBadge from '@/components/StatusBadge'
import { api } from '@/lib/api'
import type { Repository, ScanState, GlobalStats, JobRun } from '@/lib/api'

const stagger = {
  hidden: {},
  show: { transition: { staggerChildren: 0.05 } },
}

const fadeUp = {
  hidden: { opacity: 0, y: 10 },
  show: { opacity: 1, y: 0, transition: { duration: 0.3 } },
}

// ── Helpers ─────────────────────────────────────────────────────────────────

function relativeTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  const m = Math.floor(diff / 60000)
  if (m < 1) return 'just now'
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

function formatDuration(ms: number): string {
  if (ms < 1000) return '<1s'
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const rem = s % 60
  return rem > 0 ? `${m}m ${rem}s` : `${m}m`
}

const JOB_TYPE_STYLE: Record<string, string> = {
  review:    'bg-blue-50 border border-blue-200 text-blue-700 dark:bg-blue-500/15 dark:border-blue-500/20 dark:text-blue-400',
  scan:      'bg-violet-50 border border-violet-200 text-violet-700 dark:bg-violet-500/15 dark:border-violet-500/20 dark:text-violet-400',
  implement: 'bg-amber-50 border border-amber-200 text-amber-700 dark:bg-amber-500/15 dark:border-amber-500/20 dark:text-amber-400',
  rereview:  'bg-sky-50 border border-sky-200 text-sky-700 dark:bg-sky-500/15 dark:border-sky-500/20 dark:text-sky-400',
}

// ── KPI Card ─────────────────────────────────────────────────────────────────

function KpiCard({ icon: Icon, label, value, sub, accent }: {
  icon: React.ElementType
  label: string
  value: string | number
  sub?: string
  accent: string
}) {
  return (
    <motion.div variants={fadeUp} className="rounded-2xl bg-card p-5 flex flex-col gap-3">
      <div className={`h-8 w-8 rounded-xl flex items-center justify-center ${accent}`}>
        <Icon className="h-4 w-4" />
      </div>
      <div>
        <p className="text-2xl font-bold text-foreground font-mono">{value}</p>
        <p className="text-xs text-muted-foreground mt-0.5">{label}</p>
        {sub && <p className="text-xs text-muted-foreground/50 mt-0.5">{sub}</p>}
      </div>
    </motion.div>
  )
}

// ── Job Row (pipeline feed) ───────────────────────────────────────────────────

function JobRow({ job, repos }: { job: JobRun; repos: Repository[] | undefined }) {
  const repo = repos?.find(r => r.full_name === job.repo_full_name)
  const repoName = job.repo_full_name.split('/')[1]

  let statusIcon: React.ReactNode
  if (job.status === 'completed') {
    statusIcon = <CheckCircle2 className="h-4 w-4 text-emerald-600 dark:text-emerald-400 shrink-0" />
  } else if (job.status === 'failed') {
    statusIcon = <XCircle className="h-4 w-4 text-red-600 dark:text-red-400 shrink-0" />
  } else if (job.status === 'running') {
    statusIcon = <Loader2 className="h-4 w-4 text-blue-600 dark:text-blue-400 shrink-0 animate-spin" />
  } else {
    statusIcon = <Clock className="h-4 w-4 text-zinc-500 shrink-0" />
  }

  const reviewLink = repo && job.pr_number
    ? `/repos/${repo.id}/reviews/${job.pr_number}`
    : null

  const inner = (
    <div className="flex items-center gap-3 px-4 py-3 rounded-xl hover:bg-muted/50 dark:hover:bg-accent/30 transition-colors group">
      {statusIcon}
      <span className={`text-xs font-bold uppercase tracking-wider px-2.5 py-1 rounded-md shrink-0 ${JOB_TYPE_STYLE[job.type] ?? 'bg-zinc-500/15 text-zinc-600 dark:text-zinc-400'}`}>
        {job.type}
      </span>
      <div className="flex-1 min-w-0">
        <span className="text-sm text-foreground font-medium truncate">{repoName}</span>
        {job.pr_number > 0 && (
          <span className="ml-2 text-xs text-muted-foreground">#{job.pr_number}</span>
        )}
        <span className="ml-2 text-xs text-muted-foreground/50">{job.triggered_by}</span>
      </div>
      <span className="text-xs text-muted-foreground shrink-0">{formatDuration(job.duration_ms)}</span>
      <span className="text-xs text-muted-foreground/60 shrink-0 w-16 text-right">{relativeTime(job.triggered_at)}</span>
      {reviewLink && (
        <ChevronRight className="h-4 w-4 text-muted-foreground/30 group-hover:text-muted-foreground transition-colors shrink-0" />
      )}
    </div>
  )

  return (
    <motion.div variants={fadeUp}>
      {reviewLink ? <Link to={reviewLink}>{inner}</Link> : inner}
    </motion.div>
  )
}

// ── Repo Table Row ────────────────────────────────────────────────────────────

function RepoTableRow({ repo }: { repo: Repository }) {
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
    <motion.tr
      variants={fadeUp}
      className="border-b border-border last:border-0 hover:bg-muted/50 dark:border-border/20 dark:hover:bg-accent/20 transition-colors group"
    >
      <td className="py-4 pl-4 lg:pl-6">
        <Link to={`/repos/${repo.id}`} className="flex items-center gap-2.5 min-w-0">
          <div className="h-8 w-8 rounded-lg bg-accent flex items-center justify-center shrink-0 text-muted-foreground border border-border/50">
            <span className="text-xs font-bold uppercase">
              {repo.full_name.split('/')[1]?.[0] ?? '?'}
            </span>
          </div>
          <div className="min-w-0">
            <p className="text-sm font-semibold text-foreground truncate">{repo.full_name.split('/')[1]}</p>
            <p className="text-xs text-muted-foreground font-mono truncate mt-0.5">{repo.full_name}</p>
          </div>
        </Link>
      </td>
      <td className="py-4 px-4">
        <StatusBadge status={scanState?.status} size="sm" />
      </td>
      <td className="py-3 px-4 text-xs text-muted-foreground">—</td>
      <td className="py-3 px-4 text-xs text-muted-foreground">
        {scanState?.artifacts?.indexed_at
          ? new Date(scanState.artifacts.indexed_at).toLocaleDateString()
          : '—'}
      </td>
      <td className="py-3 pr-4">
        <div className="flex items-center gap-1 justify-end opacity-0 group-hover:opacity-100 transition-opacity">
          {isCompleted && (
            <Link
              to={`/repos/${repo.id}/chat`}
              className="p-1.5 rounded-lg hover:bg-primary/10 text-muted-foreground hover:text-primary transition-colors"
              title="Chat"
            >
              <MessageSquare className="h-3.5 w-3.5" />
            </Link>
          )}
          <Link
            to={`/repos/${repo.id}/reviews`}
            className="px-2.5 py-1 rounded-lg text-xs hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
          >
            Reviews
          </Link>
          <Link
            to={`/repos/${repo.id}`}
            className="p-1.5 rounded-lg hover:bg-accent text-muted-foreground transition-colors"
          >
            <ChevronRight className="h-3.5 w-3.5" />
          </Link>
        </div>
      </td>
    </motion.tr>
  )
}

// ── Dashboard ────────────────────────────────────────────────────────────────

export default function Dashboard() {
  const { data: repos, isLoading: reposLoading, isError } = useQuery<Repository[]>({
    queryKey: ['repos'],
    queryFn: api.repos.list,
  })

  const { data: globalStats } = useQuery<GlobalStats>({
    queryKey: ['global-stats'],
    queryFn: api.stats.global,
  })

  const { data: jobs, isLoading: jobsLoading } = useQuery<JobRun[]>({
    queryKey: ['jobs'],
    queryFn: () => api.jobs.list(8),
    refetchInterval: 15_000,
  })

  // Scan states for status dots
  useQueries({
    queries: (repos || []).map(repo => ({
      queryKey: ['scanState', repo.id],
      queryFn: () => api.repos.status(repo.id),
      refetchInterval: (query: { state: { data?: ScanState | null } }) => {
        const s = query.state.data?.status
        return s === 'scanning' || s === 'in_progress' || s === 'pending' ? 2000 : false
      },
    }))
  })

  if (reposLoading) {
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
        <p className="text-muted-foreground text-sm max-w-sm mb-10">
          Add a repository and trigger a review by commenting <code className="font-mono text-xs bg-accent/50 px-1.5 py-0.5 rounded">/review</code> on any GitHub PR.
        </p>
        <div className="flex items-center gap-3">
          <button
            onClick={() => {
              // Trigger the Layout add-repo dialog via a custom event
              window.dispatchEvent(new CustomEvent('open-add-repo'))
            }}
            className="flex items-center gap-2 px-4 py-2.5 rounded-xl bg-primary text-primary-foreground text-sm font-medium hover:bg-primary/90 transition-colors"
          >
            <Plus className="h-4 w-4" />
            Add Repository
          </button>
        </div>
      </div>
    )
  }

  const indexedCount = globalStats?.indexed_repos ?? 0
  const totalCount = globalStats?.total_repos ?? repos.length

  return (
    <motion.div className="space-y-8" initial="hidden" animate="show" variants={stagger}>
      {/* Header */}
      <motion.div variants={fadeUp} className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-foreground tracking-tight">Dashboard</h1>
          <p className="text-sm text-muted-foreground mt-1">AI code review pipeline overview</p>
        </div>
      </motion.div>

      {/* KPI row */}
      <motion.div variants={stagger} className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <KpiCard
          icon={Layers}
          label="Repositories"
          value={`${indexedCount}/${totalCount}`}
          sub="indexed / total"
          accent="bg-violet-500/10 border border-violet-500/20 text-violet-600 dark:text-violet-400"
        />
        <KpiCard
          icon={GitPullRequest}
          label="Reviews this week"
          value={globalStats?.reviews_this_week ?? '—'}
          accent="bg-blue-500/10 border border-blue-500/20 text-blue-600 dark:text-blue-400"
        />
        <KpiCard
          icon={AlertTriangle}
          label="Total findings"
          value={globalStats?.total_findings ?? '—'}
          sub={globalStats ? `${globalStats.findings_by_severity.critical} critical` : undefined}
          accent="bg-orange-500/10 border border-orange-500/20 text-orange-600 dark:text-orange-400"
        />
        <KpiCard
          icon={TrendingDown}
          label="Avg findings / review"
          value={globalStats?.avg_findings_per_review?.toFixed(1) ?? '—'}
          accent="bg-emerald-500/10 border border-emerald-500/20 text-emerald-600 dark:text-emerald-400"
        />
      </motion.div>

      {/* Recent Activity */}
      <motion.div variants={fadeUp} className="space-y-3">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold text-muted-foreground uppercase tracking-wider">Recent Activity</h2>
          <Link to="/jobs" className="text-xs text-primary hover:underline">View all →</Link>
        </div>
        <div className="rounded-2xl bg-card overflow-hidden border border-border shadow-sm dark:border-transparent dark:shadow-none">
          {jobsLoading ? (
            <div className="flex items-center justify-center py-10 gap-2 text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              <span className="text-sm">Loading activity...</span>
            </div>
          ) : jobs && jobs.length > 0 ? (
            <motion.div variants={stagger} className="divide-y divide-border dark:divide-border/20">
              {jobs.slice(0, 8).map(job => (
                <JobRow key={job.id} job={job} repos={repos} />
              ))}
            </motion.div>
          ) : (
            <div className="py-10 text-center text-sm text-muted-foreground">
              No activity yet — comment <code className="font-mono text-xs bg-accent/50 px-1.5 py-0.5 rounded">/review</code> on a GitHub PR to get started.
            </div>
          )}
        </div>
      </motion.div>

      {/* Repositories Table */}
      <motion.div variants={fadeUp} className="space-y-3">
        <h2 className="text-sm font-semibold text-muted-foreground uppercase tracking-wider">Repositories</h2>
        <div className="rounded-2xl bg-card overflow-hidden border border-border shadow-sm dark:border-transparent dark:shadow-none">
          <table className="w-full">
            <thead>
              <tr className="border-b border-border dark:border-border/40">
                <th className="text-left text-xs font-semibold text-muted-foreground uppercase tracking-wider py-4 pl-4 lg:pl-6">Repository</th>
                <th className="text-left text-xs font-semibold text-muted-foreground uppercase tracking-wider py-4 px-4">Status</th>
                <th className="text-left text-xs font-semibold text-muted-foreground uppercase tracking-wider py-4 px-4">Reviews</th>
                <th className="text-left text-xs font-semibold text-muted-foreground uppercase tracking-wider py-4 px-4">Last Scan</th>
                <th className="py-4 pr-4 lg:pr-6" />
              </tr>
            </thead>
            <motion.tbody variants={stagger}>
              {repos.map(repo => (
                <RepoTableRow key={repo.id} repo={repo} />
              ))}
            </motion.tbody>
          </table>
        </div>
      </motion.div>
    </motion.div>
  )
}
