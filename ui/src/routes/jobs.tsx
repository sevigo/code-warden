import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { motion } from 'framer-motion'
import {
  CheckCircle2,
  XCircle,
  Loader2,
  Clock,
  Activity,
} from 'lucide-react'
import { api } from '@/lib/api'
import type { JobRun } from '@/lib/api'

const stagger = { hidden: {}, show: { transition: { staggerChildren: 0.04 } } }
const fadeUp  = { hidden: { opacity: 0, y: 8 }, show: { opacity: 1, y: 0, transition: { duration: 0.25 } } }

type FilterType = 'all' | JobRun['type']

// ── Helpers ──────────────────────────────────────────────────────────────────

function relativeTime(iso: string): string {
  if (!iso) return '—'
  const diff = Date.now() - new Date(iso).getTime()
  const m = Math.floor(diff / 60000)
  if (m < 1) return 'just now'
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

function formatDuration(ms: number): string {
  if (!ms || ms < 1000) return '<1s'
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const rem = s % 60
  return rem > 0 ? `${m}m ${rem}s` : `${m}m`
}

const TYPE_STYLE: Record<string, string> = {
  review:    'bg-blue-500/15 text-blue-400',
  scan:      'bg-violet-500/15 text-violet-400',
  implement: 'bg-amber-500/15 text-amber-400',
  rereview:  'bg-sky-500/15 text-sky-400',
}

// ── Job Row ───────────────────────────────────────────────────────────────────

function JobRow({ job }: { job: JobRun }) {
  let statusIcon: React.ReactNode
  let statusText: string
  let statusColor: string

  switch (job.status) {
    case 'completed':
      statusIcon = <CheckCircle2 className="h-4 w-4" />
      statusText = 'Completed'
      statusColor = 'text-emerald-400'
      break
    case 'failed':
      statusIcon = <XCircle className="h-4 w-4" />
      statusText = 'Failed'
      statusColor = 'text-red-400'
      break
    case 'running':
      statusIcon = <Loader2 className="h-4 w-4 animate-spin" />
      statusText = 'Running'
      statusColor = 'text-blue-400'
      break
    default:
      statusIcon = <Clock className="h-4 w-4" />
      statusText = 'Queued'
      statusColor = 'text-zinc-500'
  }

  return (
    <motion.tr
      variants={fadeUp}
      className="border-b border-border/20 last:border-0 hover:bg-accent/20 transition-colors group"
    >
      {/* Type */}
      <td className="py-3 pl-4">
        <span className={`text-[10px] font-bold uppercase tracking-wider px-2 py-0.5 rounded-md ${TYPE_STYLE[job.type] ?? 'bg-zinc-500/15 text-zinc-400'}`}>
          {job.type}
        </span>
      </td>

      {/* Repo */}
      <td className="py-3 px-4">
        <div className="text-sm text-foreground font-medium truncate max-w-[180px]">
          {job.repo_full_name.split('/')[1]}
        </div>
        <div className="text-[10px] text-muted-foreground/50 font-mono">{job.repo_full_name}</div>
      </td>

      {/* PR */}
      <td className="py-3 px-4 text-sm font-mono text-muted-foreground">
        {job.pr_number > 0 ? `#${job.pr_number}` : <span className="text-muted-foreground/30">—</span>}
      </td>

      {/* Status */}
      <td className="py-3 px-4">
        <div className={`flex items-center gap-1.5 text-xs font-medium ${statusColor}`}>
          {statusIcon}
          {statusText}
        </div>
      </td>

      {/* Triggered by */}
      <td className="py-3 px-4 text-xs text-muted-foreground/60 font-mono truncate max-w-[140px]">
        {job.triggered_by}
      </td>

      {/* Duration */}
      <td className="py-3 px-4 text-xs text-muted-foreground tabular-nums">
        {formatDuration(job.duration_ms)}
      </td>

      {/* Time */}
      <td className="py-3 pr-4 text-xs text-muted-foreground/60 text-right tabular-nums">
        {relativeTime(job.triggered_at)}
      </td>
    </motion.tr>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

export default function JobsPage() {
  const [filter, setFilter] = useState<FilterType>('all')

  const { data: jobs, isLoading } = useQuery<JobRun[]>({
    queryKey: ['jobs'],
    queryFn: () => api.jobs.list(50),
    refetchInterval: 15_000,
  })

  const filtered = jobs?.filter(j => filter === 'all' || j.type === filter) ?? []

  const FILTERS: { key: FilterType; label: string }[] = [
    { key: 'all',        label: 'All' },
    { key: 'review',     label: 'Review' },
    { key: 'scan',       label: 'Scan' },
    { key: 'implement',  label: 'Implement' },
    { key: 'rereview',   label: 'Re-review' },
  ]

  const stats = {
    total:     jobs?.length ?? 0,
    completed: jobs?.filter(j => j.status === 'completed').length ?? 0,
    failed:    jobs?.filter(j => j.status === 'failed').length ?? 0,
    running:   jobs?.filter(j => j.status === 'running').length ?? 0,
  }

  return (
    <motion.div className="space-y-6" initial="hidden" animate="show" variants={stagger}>
      {/* Header */}
      <motion.div variants={fadeUp} className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-foreground">Activity</h1>
          <p className="text-sm text-muted-foreground mt-0.5">All pipeline jobs across repositories</p>
        </div>
        {stats.running > 0 && (
          <div className="flex items-center gap-2 text-xs text-blue-400 bg-blue-500/10 px-3 py-1.5 rounded-xl">
            <Activity className="h-3.5 w-3.5 animate-pulse" />
            {stats.running} running
          </div>
        )}
      </motion.div>

      {/* Mini stats */}
      <motion.div variants={fadeUp} className="flex items-center gap-4 text-xs text-muted-foreground">
        <span>{stats.total} total</span>
        <span className="text-emerald-400">{stats.completed} completed</span>
        {stats.failed > 0 && <span className="text-red-400">{stats.failed} failed</span>}
      </motion.div>

      {/* Filter tabs */}
      <motion.div variants={fadeUp} className="flex items-center gap-1 bg-card rounded-xl p-1 w-fit">
        {FILTERS.map(f => (
          <button
            key={f.key}
            onClick={() => setFilter(f.key)}
            className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-all ${
              filter === f.key
                ? 'bg-primary/10 text-primary'
                : 'text-muted-foreground hover:text-foreground hover:bg-accent/50'
            }`}
          >
            {f.label}
            {f.key !== 'all' && jobs && (
              <span className="ml-1.5 text-muted-foreground/40">
                {jobs.filter(j => j.type === f.key).length}
              </span>
            )}
          </button>
        ))}
      </motion.div>

      {/* Table */}
      {isLoading ? (
        <div className="flex items-center justify-center py-20 gap-2 text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          <span className="text-sm">Loading activity...</span>
        </div>
      ) : filtered.length === 0 ? (
        <div className="rounded-2xl bg-card p-12 text-center">
          <p className="text-sm text-muted-foreground">No jobs yet.</p>
        </div>
      ) : (
        <motion.div variants={fadeUp} className="rounded-2xl bg-card overflow-hidden">
          <table className="w-full">
            <thead>
              <tr className="border-b border-border/30">
                <th className="text-left text-[10px] font-semibold text-muted-foreground uppercase tracking-wider py-3 pl-4">Type</th>
                <th className="text-left text-[10px] font-semibold text-muted-foreground uppercase tracking-wider py-3 px-4">Repository</th>
                <th className="text-left text-[10px] font-semibold text-muted-foreground uppercase tracking-wider py-3 px-4">PR</th>
                <th className="text-left text-[10px] font-semibold text-muted-foreground uppercase tracking-wider py-3 px-4">Status</th>
                <th className="text-left text-[10px] font-semibold text-muted-foreground uppercase tracking-wider py-3 px-4">Triggered by</th>
                <th className="text-left text-[10px] font-semibold text-muted-foreground uppercase tracking-wider py-3 px-4">Duration</th>
                <th className="text-right text-[10px] font-semibold text-muted-foreground uppercase tracking-wider py-3 pr-4">When</th>
              </tr>
            </thead>
            <motion.tbody variants={stagger}>
              {filtered.map(job => <JobRow key={job.id} job={job} />)}
            </motion.tbody>
          </table>
        </motion.div>
      )}
    </motion.div>
  )
}
