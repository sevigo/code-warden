import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { motion } from 'framer-motion'
import {
  CheckCircle2,
  XCircle,
  Loader2,
  Clock,
  Activity,
  RotateCcw,
  GitPullRequest,
  Layers,
  Zap,
  Filter,
  ChevronDown,
  ChevronUp,
  RefreshCw,
} from 'lucide-react'
import { api } from '@/lib/api'
import type { JobRun } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { cn } from '@/lib/utils'

const stagger = { hidden: {}, show: { transition: { staggerChildren: 0.04 } } }
const fadeUp = { hidden: { opacity: 0, y: 8 }, show: { opacity: 1, y: 0, transition: { duration: 0.25 } } }

type FilterType = 'all' | 'review' | 'scan' | 'implement' | 'rereview'
type StatusFilter = 'all' | 'completed' | 'failed' | 'running' | 'queued'

// ── Helper Functions ─────────────────────────────────────────────────────────

function relativeTime(iso: string): string {
  if (!iso) return '—'
  const diff = Date.now() - new Date(iso).getTime()
  const m = Math.floor(diff / 60000)
  if (m < 1) return 'just now'
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h`
  return `${Math.floor(h / 24)}d`
}

function formatDuration(ms: number): string {
  if (!ms || ms < 1000) return '<1s'
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const rem = s % 60
  return rem > 0 ? `${m}m ${rem}s` : `${m}m`
}

// ── Status Badge Component ───────────────────────────────────────────────────

function JobStatusBadge({ status }: { status: JobRun['status'] }) {
  const configs: Record<string, { icon: typeof CheckCircle2; label: string; className: string }> = {
    completed: {
      icon: CheckCircle2,
      label: 'Completed',
      className: 'text-emerald-500 bg-emerald-500/10',
    },
    failed: {
      icon: XCircle,
      label: 'Failed',
      className: 'text-rose-500 bg-rose-500/10',
    },
    running: {
      icon: Loader2,
      label: 'Running',
      className: 'text-blue-500 bg-blue-500/10',
    },
    queued: {
      icon: Clock,
      label: 'Queued',
      className: 'text-[#8c919b] bg-[#f1f2f3] dark:bg-[#1e2025]',
    },
    pending: {
      icon: Clock,
      label: 'Pending',
      className: 'text-[#8c919b] bg-[#f1f2f3] dark:bg-[#1e2025]',
    },
  }

  const config = configs[status] || configs.pending
  const Icon = config.icon

  return (
    <span className={cn(
      'inline-flex items-center gap-1.5 text-xs font-medium px-2 py-1 rounded-[5px]',
      config.className
    )}>
      <Icon className={cn('h-3 w-3', status === 'running' && 'animate-spin')} />
      {config.label}
    </span>
  )
}

// ── Type Badge Component ─────────────────────────────────────────────────────

function JobTypeBadge({ type }: { type: JobRun['type'] }) {
  const configs = {
    review: {
      icon: GitPullRequest,
      className: 'text-blue-500 bg-blue-500/10 border-blue-500/20',
    },
    scan: {
      icon: Layers,
      className: 'text-violet-500 bg-violet-500/10 border-violet-500/20',
    },
    implement: {
      icon: Zap,
      className: 'text-amber-500 bg-amber-500/10 border-amber-500/20',
    },
    rereview: {
      icon: RotateCcw,
      className: 'text-sky-500 bg-sky-500/10 border-sky-500/20',
    },
  }

  const config = configs[type]
  const Icon = config.icon

  return (
    <span className={cn(
      'inline-flex items-center gap-1.5 text-xs font-bold uppercase tracking-wider px-2 py-1 rounded-[5px] border',
      config.className
    )}>
      <Icon className="h-3 w-3" />
      {type}
    </span>
  )
}

// ── Job Row Component ────────────────────────────────────────────────────────

function JobRow({ job }: { job: JobRun }) {
  const repoName = job.repo_full_name.split('/')[1]

  return (
    <motion.tr
      variants={fadeUp}
      className="border-b border-[#e1e3e6] dark:border-[#2d2f36] last:border-0 hover:bg-[#f1f2f3]/50 dark:hover:bg-[#1e2025]/50 transition-colors group"
    >
      {/* Type */}
      <td className="py-3 pl-4 lg:pl-5">
        <JobTypeBadge type={job.type} />
      </td>

      {/* Repository */}
      <td className="py-3 px-3">
        <div>
          <div className="text-sm font-medium text-foreground truncate max-w-[180px]">
            {repoName}
          </div>
          <div className="text-xs text-[#8c919b] font-mono mt-0.5">
            {job.repo_full_name}
          </div>
        </div>
      </td>

      {/* PR */}
      <td className="py-3 px-3 text-sm font-mono text-[#8c919b]">
        {job.pr_number > 0 ? `#${job.pr_number}` : '—'}
      </td>

      {/* Status */}
      <td className="py-3 px-3">
        <JobStatusBadge status={job.status} />
      </td>

      {/* Triggered By */}
      <td className="py-3 px-3 text-xs text-[#8c919b] font-mono truncate max-w-[140px]">
        {job.triggered_by}
      </td>

      {/* Duration */}
      <td className="py-3 px-3 text-sm text-foreground tabular-nums">
        {formatDuration(job.duration_ms)}
      </td>

      {/* Time */}
      <td className="py-3 pr-4 lg:pr-5 text-xs text-[#8c919b] text-right tabular-nums">
        {relativeTime(job.triggered_at)}
      </td>
    </motion.tr>
  )
}

// ── Stat Card Component ───────────────────────────────────────────────────────

function StatCard({
  icon: Icon,
  label,
  value,
  color,
}: {
  icon: React.ElementType
  label: string
  value: number
  color: 'emerald' | 'blue' | 'rose' | 'amber' | 'neutral'
}) {
  const colorClasses = {
    emerald: 'text-emerald-500 bg-emerald-500/10',
    blue: 'text-blue-500 bg-blue-500/10',
    rose: 'text-rose-500 bg-rose-500/10',
    amber: 'text-amber-500 bg-amber-500/10',
    neutral: 'text-[#8c919b] bg-[#f1f2f3] dark:bg-[#1e2025]',
  }

  return (
    <Card className="p-4 flex items-center gap-3">
      <div className={cn('h-9 w-9 rounded-[6px] flex items-center justify-center shrink-0', colorClasses[color])}>
        <Icon className="h-4 w-4" />
      </div>
      <div>
        <p className="text-lg font-bold text-foreground">{value}</p>
        <p className="text-xs text-[#8c919b]">{label}</p>
      </div>
    </Card>
  )
}

// ── Main Page Component ───────────────────────────────────────────────────────

export default function JobsPage() {
  const [typeFilter, setTypeFilter] = useState<FilterType>('all')
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
  const [showFilters, setShowFilters] = useState(false)

  const { data: jobs, isLoading, refetch } = useQuery<JobRun[]>({
    queryKey: ['jobs'],
    queryFn: () => api.jobs.list(50),
    refetchInterval: 15_000,
  })

  const filtered = jobs?.filter((j) => {
    if (typeFilter !== 'all' && j.type !== typeFilter) return false
    if (statusFilter !== 'all' && j.status !== statusFilter) return false
    return true
  }) ?? []

  const stats = {
    total: jobs?.length ?? 0,
    completed: jobs?.filter((j) => j.status === 'completed').length ?? 0,
    failed: jobs?.filter((j) => j.status === 'failed').length ?? 0,
    running: jobs?.filter((j) => j.status === 'running').length ?? 0,
  }

  const FILTERS: { key: FilterType; label: string }[] = [
    { key: 'all', label: 'All Types' },
    { key: 'review', label: 'Review' },
    { key: 'scan', label: 'Scan' },
    { key: 'implement', label: 'Implement' },
    { key: 'rereview', label: 'Re-review' },
  ]

  const STATUS_FILTERS: { key: StatusFilter; label: string }[] = [
    { key: 'all', label: 'All Statuses' },
    { key: 'running', label: 'Running' },
    { key: 'completed', label: 'Completed' },
    { key: 'failed', label: 'Failed' },
    { key: 'queued', label: 'Queued' },
  ]

  return (
    <motion.div className="space-y-6" initial="hidden" animate="show" variants={stagger}>
      {/* Header */}
      <motion.div variants={fadeUp} className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-foreground">Activity</h1>
          <p className="text-sm text-[#8c919b] mt-0.5">Pipeline jobs across all repositories</p>
        </div>
        <div className="flex items-center gap-2">
          {stats.running > 0 && (
            <span className="flex items-center gap-1.5 text-xs text-blue-500 bg-blue-500/10 px-3 py-1.5 rounded-[5px]">
              <Activity className="h-3.5 w-3.5 animate-pulse" />
              {stats.running} running
            </span>
          )}
          <Button variant="ghost" size="icon" onClick={() => refetch()} title="Refresh">
            <RefreshCw className="h-4 w-4" />
          </Button>
        </div>
      </motion.div>

      {/* Stats */}
      <motion.div variants={fadeUp} className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <StatCard icon={Activity} label="Total Jobs" value={stats.total} color="blue" />
        <StatCard icon={CheckCircle2} label="Completed" value={stats.completed} color="emerald" />
        <StatCard icon={XCircle} label="Failed" value={stats.failed} color="rose" />
        <StatCard icon={Loader2} label="Running" value={stats.running} color="amber" />
      </motion.div>

      {/* Filters */}
      <motion.div variants={fadeUp} className="space-y-3">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setShowFilters(!showFilters)}
              className="text-[#8c919b] hover:text-foreground"
            >
              <Filter className="h-4 w-4 mr-1.5" />
              Filters
              {showFilters ? <ChevronUp className="h-3 w-3 ml-1" /> : <ChevronDown className="h-3 w-3 ml-1" />}
            </Button>
            {(typeFilter !== 'all' || statusFilter !== 'all') && (
              <span className="text-xs text-[#8c919b]">
                Showing {filtered.length} of {jobs?.length ?? 0} jobs
              </span>
            )}
          </div>
          <span className="text-xs text-[#8c919b]">
            Auto-updates every 15s
          </span>
        </div>

        {showFilters && (
          <div className="flex flex-wrap items-center gap-2 p-3 bg-white dark:bg-[#15181e] rounded-[6px] border border-[#e1e3e6] dark:border-[#2d2f36]">
            {/* Type Filter */}
            <div className="flex items-center gap-1">
              <span className="text-xs text-[#8c919b] mr-1">Type:</span>
              {FILTERS.map((f) => (
                <button
                  key={f.key}
                  onClick={() => setTypeFilter(f.key)}
                  className={cn(
                    'px-2 py-1 rounded-[4px] text-xs font-medium transition-colors',
                    typeFilter === f.key
                      ? 'bg-[#2264d6]/10 text-[#2264d6]'
                      : 'text-[#8c919b] hover:bg-[#f1f2f3] dark:hover:bg-[#2d2f36]'
                  )}
                >
                  {f.label}
                </button>
              ))}
            </div>

            <div className="w-px h-6 bg-[#e1e3e6] dark:bg-[#2d2f36] mx-1" />

            {/* Status Filter */}
            <div className="flex items-center gap-1">
              <span className="text-xs text-[#8c919b] mr-1">Status:</span>
              {STATUS_FILTERS.map((f) => (
                <button
                  key={f.key}
                  onClick={() => setStatusFilter(f.key)}
                  className={cn(
                    'px-2 py-1 rounded-[4px] text-xs font-medium transition-colors',
                    statusFilter === f.key
                      ? 'bg-[#2264d6]/10 text-[#2264d6]'
                      : 'text-[#8c919b] hover:bg-[#f1f2f3] dark:hover:bg-[#2d2f36]'
                  )}
                >
                  {f.label}
                </button>
              ))}
            </div>
          </div>
        )}
      </motion.div>

      {/* Table */}
      {isLoading ? (
        <div className="flex items-center justify-center py-20 gap-2 text-[#8c919b]">
          <Loader2 className="h-4 w-4 animate-spin" />
          <span className="text-sm">Loading activity...</span>
        </div>
      ) : filtered.length === 0 ? (
        <Card className="p-12 text-center">
          <div className="h-12 w-12 rounded-2xl bg-[#f1f2f3] dark:bg-[#1e2025] flex items-center justify-center mx-auto mb-4">
            <Activity className="h-6 w-6 text-[#8c919b]" />
          </div>
          <p className="text-sm font-medium text-foreground mb-1">
            {jobs?.length === 0 ? 'No jobs yet' : 'No matching jobs'}
          </p>
          <p className="text-sm text-[#8c919b]">
            {jobs?.length === 0
              ? 'Jobs will appear here when you trigger reviews or scans'
              : 'Try adjusting your filters'}
          </p>
        </Card>
      ) : (
        <motion.div variants={fadeUp}>
          <Card className="overflow-hidden">
            <table className="w-full">
              <thead>
                <tr className="border-b border-[#e1e3e6] dark:border-[#2d2f36]">
                  <th className="text-left text-[10px] font-semibold text-[#8c919b] uppercase tracking-wider py-3 pl-4 lg:pl-5">
                    Type
                  </th>
                  <th className="text-left text-[10px] font-semibold text-[#8c919b] uppercase tracking-wider py-3 px-3">
                    Repository
                  </th>
                  <th className="text-left text-[10px] font-semibold text-[#8c919b] uppercase tracking-wider py-3 px-3">
                    PR
                  </th>
                  <th className="text-left text-[10px] font-semibold text-[#8c919b] uppercase tracking-wider py-3 px-3">
                    Status
                  </th>
                  <th className="text-left text-[10px] font-semibold text-[#8c919b] uppercase tracking-wider py-3 px-3">
                    Triggered By
                  </th>
                  <th className="text-left text-[10px] font-semibold text-[#8c919b] uppercase tracking-wider py-3 px-3">
                    Duration
                  </th>
                  <th className="text-right text-[10px] font-semibold text-[#8c919b] uppercase tracking-wider py-3 pr-4 lg:pr-5">
                    When
                  </th>
                </tr>
              </thead>
              <motion.tbody variants={stagger}>
                {filtered.map((job) => (
                  <JobRow key={job.id} job={job} />
                ))}
              </motion.tbody>
            </table>
          </Card>
        </motion.div>
      )}
    </motion.div>
  )
}
