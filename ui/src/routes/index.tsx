import { useState } from 'react'
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
  Activity,
  Zap,
  GitBranch,
} from 'lucide-react'
import StatusBadge from '@/components/StatusBadge'
import { api } from '@/lib/api'
import type { Repository, ScanState, GlobalStats, JobRun } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { KPICard, ActionCard } from '@/components/ui/card'

/**
 * CircleCI-Inspired Dashboard
 * 
 * Design patterns from CircleCI:
 * - Pipeline visualization with clear status indicators
 * - Recent builds activity feed
 * - Project list with quick actions
 * - Organized layout with visual hierarchy
 * - Status-based color coding
 */

const stagger = {
  hidden: {},
  show: { transition: { staggerChildren: 0.05 } },
}

const fadeUp = {
  hidden: { opacity: 0, y: 10 },
  show: { opacity: 1, y: 0, transition: { duration: 0.3 } },
}

// ── Helper Functions ────────────────────────────────────────────────────────

function relativeTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  const m = Math.floor(diff / 60000)
  if (m < 1) return 'just now'
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h`
  return `${Math.floor(h / 24)}d`
}

function formatDuration(ms: number): string {
  if (ms < 1000) return '<1s'
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const rem = s % 60
  return rem > 0 ? `${m}m ${rem}s` : `${m}m`
}

// ── Components ─────────────────────────────────────────────────────────────

/**
 * Pipeline Activity Row - CircleCI-style job feed
 */
function PipelineRow({ job, repos }: { job: JobRun; repos: Repository[] | undefined }) {
  const repo = repos?.find(r => r.full_name === job.repo_full_name)
  const repoName = job.repo_full_name.split('/')[1]
  
  const getStatusIcon = () => {
    switch (job.status) {
      case 'completed':
        return <CheckCircle2 className="h-4 w-4 text-emerald-500" />
      case 'failed':
        return <XCircle className="h-4 w-4 text-rose-500" />
      case 'running':
        return <Loader2 className="h-4 w-4 text-blue-500 animate-spin" />
      default:
        return <Clock className="h-4 w-4 text-[#8c919b]" />
    }
  }
  
  const getTypeColor = () => {
    switch (job.type) {
      case 'review': return 'text-blue-500'
      case 'scan': return 'text-violet-500'
      case 'implement': return 'text-amber-500'
      case 'rereview': return 'text-sky-500'
      default: return 'text-[#8c919b]'
    }
  }

  const reviewLink = repo && job.pr_number
    ? `/repos/${repo.id}/reviews/${job.pr_number}`
    : null

  return (
    <motion.div 
      variants={fadeUp}
      className="group"
    >
      <Link to={reviewLink || '#'} className="block">
        <div className="flex items-center gap-3 px-4 py-3 hover:bg-[#f1f2f3] dark:hover:bg-[#1e2025] rounded-[6px] transition-colors">
          {/* Status Icon */}
          <div className="shrink-0">
            {getStatusIcon()}
          </div>
          
          {/* Type Badge */}
          <span className={`text-[10px] font-bold uppercase tracking-wider px-2 py-0.5 rounded-[4px] bg-[#f1f2f3] dark:bg-[#1e2025] ${getTypeColor()}`}>
            {job.type}
          </span>
          
          {/* Repository */}
          <div className="flex-1 min-w-0">
            <span className="text-sm font-medium text-foreground truncate">{repoName}</span>
            {job.pr_number > 0 && (
              <span className="text-xs text-[#8c919b] ml-2">
                #{job.pr_number}
              </span>
            )}
          </div>
          
          {/* Trigger */}
          <span className="text-xs text-[#8c919b] truncate max-w-[120px] hidden sm:block">
            {job.triggered_by}
          </span>
          
          {/* Duration */}
          <span className="text-xs text-foreground tabular-nums shrink-0">
            {formatDuration(job.duration_ms)}
          </span>
          
          {/* Time */}
          <span className="text-xs text-[#8c919b] tabular-nums shrink-0 w-12 text-right">
            {relativeTime(job.triggered_at)}
          </span>
          
          {/* Arrow */}
          {reviewLink && (
            <ChevronRight className="h-4 w-4 text-[#d5d7db] opacity-0 group-hover:opacity-100 transition-opacity shrink-0" />
          )}
        </div>
      </Link>
    </motion.div>
  )
}

/**
 * Repository Row - Enhanced table row with actions
 */
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
    <motion.tr
      variants={fadeUp}
      className="group border-b border-[#e1e3e6] last:border-0 dark:border-[#2d2f36] hover:bg-[#f1f2f3]/50 dark:hover:bg-[#1e2025]/50 transition-colors"
    >
      {/* Repo Name */}
      <td className="py-3 pl-4 lg:pl-5">
        <Link to={`/repos/${repo.id}`} className="flex items-center gap-3">
          <div className="h-8 w-8 rounded-[6px] bg-[#f1f2f3] dark:bg-[#1e2025] flex items-center justify-center shrink-0">
            <span className="text-xs font-bold text-[#656a76] uppercase">
              {repo.full_name.split('/')[1]?.[0] ?? '?'}
            </span>
          </div>
          <div className="min-w-0">
            <p className="text-sm font-medium text-foreground truncate">
              {repo.full_name.split('/')[1]}
            </p>
            <p className="text-xs text-[#8c919b] font-mono truncate">
              {repo.full_name}
            </p>
          </div>
        </Link>
      </td>
      
      {/* Status */}
      <td className="py-3 px-3">
        <StatusBadge status={scanState?.status} size="sm" />
      </td>
      
      {/* Reviews */}
      <td className="py-3 px-3">
        <span className="text-xs text-[#8c919b]">—</span>
      </td>
      
      {/* Last Scan */}
      <td className="py-3 px-3">
        <span className="text-xs text-[#8c919b]">
          {scanState?.artifacts?.indexed_at
            ? new Date(scanState.artifacts.indexed_at).toLocaleDateString(undefined, { 
                month: 'short', 
                day: 'numeric' 
              })
            : '—'}
        </span>
      </td>
      
      {/* Actions */}
      <td className="py-3 pr-4 lg:pr-5">
        <div className="flex items-center justify-end gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
          {isCompleted && (
            <Link
              to={`/repos/${repo.id}/chat`}
              className="p-1.5 rounded-[4px] hover:bg-[#2264d6]/10 text-[#8c919b] hover:text-[#2264d6] transition-colors"
              title="Chat"
            >
              <MessageSquare className="h-3.5 w-3.5" />
            </Link>
          )}
          <Link
            to={`/repos/${repo.id}/reviews`}
            className="px-2.5 py-1.5 rounded-[4px] text-xs font-medium text-[#656a76] hover:text-foreground hover:bg-[#e1e3e6]/50 dark:text-[#b2b6bd] dark:hover:bg-[#2d2f36] transition-colors"
          >
            Reviews
          </Link>
          
          <Link
            to={`/repos/${repo.id}`}
            className="p-1.5 rounded-[4px] hover:bg-[#e1e3e6]/50 dark:hover:bg-[#2d2f36] text-[#8c919b] transition-colors"
          >
            <ChevronRight className="h-3.5 w-3.5" />
          </Link>
        </div>
      </td>
    </motion.tr>
  )
}

/**
 * Empty State - When no repositories exist
 */
function EmptyState({ onAdd }: { onAdd: () => void }) {
  return (
    <motion.div 
      initial={{ opacity: 0, y: 20 }}
      animate={{ opacity: 1, y: 0 }}
      className="flex flex-col items-center justify-center py-20 text-center"
    >
      <div className="relative mb-6">
        <div className="absolute inset-0 rounded-3xl bg-[#2264d6]/10 blur-2xl scale-150" />
        <div className="relative h-20 w-20 rounded-2xl bg-[#2264d6]/10 flex items-center justify-center">
          <Shield className="h-10 w-10 text-[#2264d6]" />
        </div>
      </div>
      
      <h1 className="text-2xl font-bold text-foreground mb-2">
        Welcome to Code Warden
      </h1>
      
      <p className="text-[#656a76] text-sm max-w-sm mb-8">
        Add a repository and trigger a review by commenting{' '}
        <code className="font-mono text-xs bg-[#f1f2f3] px-1.5 py-0.5 rounded dark:bg-[#1e2025]">
          /review
        </code>
        {' '}on any GitHub PR.
      </p>
      
      <Button onClick={onAdd} size="lg">
        <Plus className="h-4 w-4 mr-2" />
        Add Your First Repository
      </Button>
      
      <p className="mt-4 text-xs text-[#8c919b]">
        or click the + button in the sidebar
      </p>
    </motion.div>
  )
}

// ── Main Dashboard Component ────────────────────────────────────────────────

export default function Dashboard() {
  const [, setShowAddDialog] = useState(false)
  
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

  // Prefetch scan states
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
        <Loader2 className="h-6 w-6 animate-spin text-[#2264d6]" />
        <p className="text-sm text-[#656a76]">Loading workspace...</p>
      </div>
    )
  }

  if (isError) {
    return (
      <div className="flex flex-col items-center justify-center py-32 text-center gap-4">
        <div className="h-12 w-12 rounded-2xl bg-rose-500/10 flex items-center justify-center">
          <Shield className="h-6 w-6 text-rose-500" />
        </div>
        <p className="text-sm text-[#656a76]">Failed to load repositories.</p>
      </div>
    )
  }

  // Empty state when no repos
  if (!repos || repos.length === 0) {
    return <EmptyState onAdd={() => setShowAddDialog(true)} />
  }

  const indexedCount = globalStats?.indexed_repos ?? 0
  const totalCount = globalStats?.total_repos ?? repos.length

  return (
    <motion.div 
      className="space-y-6" 
      initial="hidden" 
      animate="show" 
      variants={stagger}
    >
      {/* Header */}
      <motion.div variants={fadeUp} className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-foreground">Dashboard</h1>
          <p className="text-sm text-[#656a76] mt-0.5">AI code review pipeline overview</p>
        </div>
        
        <div className="flex items-center gap-2">
          <Button onClick={() => setShowAddDialog(true)} variant="secondary">
            <Plus className="h-4 w-4 mr-1.5" />
            Add Repository
          </Button>
        </div>
      </motion.div>

      {/* KPI Cards */}
      <motion.div variants={stagger} className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <KPICard
          icon={Layers}
          label="Repositories"
          value={`${indexedCount}/${totalCount}`}
          sub="indexed / total"
          color="text-violet-500"
        />

        <KPICard
          icon={GitPullRequest}
          label="Reviews this week"
          value={globalStats?.reviews_this_week ?? '—'}
          color="text-blue-500"
        />

        <KPICard
          icon={AlertTriangle}
          label="Total findings"
          value={globalStats?.total_findings ?? '—'}
          sub={globalStats ? `${globalStats.findings_by_severity.critical} critical` : undefined}
          color="text-amber-500"
        />

        <KPICard
          icon={TrendingDown}
          label="Avg findings / review"
          value={globalStats?.avg_findings_per_review?.toFixed(1) ?? '—'}
          color="text-emerald-500"
        />
      </motion.div>

      {/* Content Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        <div className="lg:col-span-2 space-y-6">
          {/* Recent Activity */}
          <motion.div variants={fadeUp} className="space-y-3">
            <div className="flex items-center justify-between">
              <h2 className="text-xs font-semibold text-[#656a76] uppercase tracking-wider">
                Recent Activity
              </h2>
              <Link to="/jobs" className="text-xs text-[#2264d6] hover:underline dark:text-[#2b89ff]">
                View all →
              </Link>
            </div>
            
            <div className="bg-white dark:bg-[#15181e] rounded-[8px] border border-[#e1e3e6] dark:border-[#2d2f36] shadow-[0_1px_1px_rgba(97,104,117,0.05)] overflow-hidden">
              {jobsLoading ? (
                <div className="flex items-center justify-center py-10 gap-2 text-[#8c919b]">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  <span className="text-sm">Loading activity...</span>
                </div>
              ) : jobs && jobs.length > 0 ? (
                <motion.div variants={stagger}>
                  {jobs.slice(0, 8).map(job => (
                    <PipelineRow key={job.id} job={job} repos={repos} />
                  ))}
                </motion.div>
              ) : (
                <div className="py-10 text-center text-sm text-[#8c919b]">
                  No activity yet — comment{' '}
                  <code className="font-mono text-xs bg-[#f1f2f3] px-1.5 py-0.5 rounded dark:bg-[#1e2025]">
                    /review
                  </code>
                  {' '}on a GitHub PR to get started.
                </div>
              )}
            </div>
          </motion.div>
          
          {/* Repositories Table */}
          <motion.div variants={fadeUp} className="space-y-3">
            <div className="flex items-center justify-between">
              <h2 className="text-xs font-semibold text-[#656a76] uppercase tracking-wider">
                Repositories
              </h2>
              <span className="text-xs text-[#8c919b]">
                {repos.length} total
              </span>
            </div>
            
            <div className="bg-white dark:bg-[#15181e] rounded-[8px] border border-[#e1e3e6] dark:border-[#2d2f36] shadow-[0_1px_1px_rgba(97,104,117,0.05)] overflow-hidden">
              <table className="w-full">
                <thead>
                  <tr className="border-b border-[#e1e3e6] dark:border-[#2d2f36]">
                    <th className="text-left text-[10px] font-semibold text-[#656a76] uppercase tracking-wider py-3 pl-4 lg:pl-5">Repository</th>
                    <th className="text-left text-[10px] font-semibold text-[#656a76] uppercase tracking-wider py-3 px-3">Status</th>
                    <th className="text-left text-[10px] font-semibold text-[#656a76] uppercase tracking-wider py-3 px-3">Reviews</th>
                    <th className="text-left text-[10px] font-semibold text-[#656a76] uppercase tracking-wider py-3 px-3">Last Scan</th>
                    <th className="py-3 pr-4 lg:pr-5" />
                  </tr>
                </thead>
                <motion.tbody variants={stagger}>
                  {repos.map(repo => (
                    <RepoRow key={repo.id} repo={repo} />
                  ))}
                </motion.tbody>
              </table>
            </div>
          </motion.div>
        </div>
        
        {/* Quick Actions */}
        <div className="space-y-4">
          <motion.div variants={fadeUp}>
            <ActionCard
              icon={Zap}
              title="Quick Start"
              description="Set up your first repository and trigger an initial scan to begin AI-powered code reviews."
              actionLabel="Get Started"
              href="/setup"
            />
          </motion.div>
          
          <motion.div variants={fadeUp}>
            <ActionCard
              icon={GitBranch}
              title="Review Workflow"
              description="Learn how to trigger reviews with /review and /rereview commands on GitHub PRs."
              actionLabel="Learn More"
              href="#"
            />
          </motion.div>
          
          <motion.div variants={fadeUp} className="bg-[#2264d6]/5 rounded-[8px] border border-[#2264d6]/20 p-5">
            <div className="flex items-start gap-3">
              <div className="h-8 w-8 rounded-[6px] bg-[#2264d6]/10 flex items-center justify-center shrink-0">
                <Activity className="h-4 w-4 text-[#2264d6]" />
              </div>
              
              <div>
                <h3 className="font-semibold text-foreground text-sm mb-1">System Status</h3>
                <div className="space-y-2 mt-3">
                  <div className="flex items-center gap-2 text-xs">
                    <div className="h-2 w-2 rounded-full bg-emerald-500" />
                    <span className="text-[#656a76]">API Server</span>
                  </div>
                  <div className="flex items-center gap-2 text-xs">
                    <div className="h-2 w-2 rounded-full bg-emerald-500" />
                    <span className="text-[#656a76]">Database</span>
                  </div>
                  <div className="flex items-center gap-2 text-xs">
                    <div className="h-2 w-2 rounded-full bg-emerald-500" />
                    <span className="text-[#656a76]">Vector Store</span>
                  </div>
                </div>
              </div>
            </div>
          </motion.div>
        </div>
      </div>
    </motion.div>
  )
}
