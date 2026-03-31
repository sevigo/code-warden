import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useParams, Link } from 'react-router-dom'
import { motion } from 'framer-motion'
import {
  ArrowLeft,
  MessageSquare,
  RefreshCw,
  Layers,
  FileCode,
  Hash,
  CalendarDays,
  Loader2,
  GitPullRequest,
  ChevronRight,
  CheckCircle2,
  Circle,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import StatusBadge from '@/components/StatusBadge'
import { api } from '@/lib/api'
import type { Repository, ScanState, RepoStats, ReviewSummary } from '@/lib/api'
import { useScanProgress } from '@/lib/useScanProgress'

const stagger = { hidden: {}, show: { transition: { staggerChildren: 0.05 } } }
const fadeUp = { hidden: { opacity: 0, y: 10 }, show: { opacity: 1, y: 0, transition: { duration: 0.3 } } }

// ── Pipeline stages ──────────────────────────────────────────────────────────

const STAGES = ['Index', 'Context Build', 'Review Ready', 'Post to GitHub']

function PipelineBar({ status }: { status: string | undefined }) {
  const completedIdx = status === 'completed' ? 3 : status === 'scanning' || status === 'in_progress' ? 1 : -1

  return (
    <div className="flex items-center gap-0">
      {STAGES.map((stage, i) => {
        const done = i <= completedIdx
        const active = i === completedIdx + 1 && (status === 'scanning' || status === 'in_progress')
        return (
          <div key={stage} className="flex items-center flex-1 min-w-0">
            <div className={`
              flex items-center gap-1.5 px-3 py-2 rounded-lg text-xs font-medium flex-1 justify-center
              ${done ? 'bg-emerald-500/10 text-emerald-400' : active ? 'bg-blue-500/10 text-blue-400 animate-pulse' : 'bg-accent/30 text-muted-foreground/50'}
            `}>
              {done ? (
                <CheckCircle2 className="h-3 w-3 shrink-0" />
              ) : active ? (
                <Loader2 className="h-3 w-3 shrink-0 animate-spin" />
              ) : (
                <Circle className="h-3 w-3 shrink-0" />
              )}
              <span className="truncate hidden sm:block">{stage}</span>
            </div>
            {i < STAGES.length - 1 && (
              <div className={`h-px w-4 shrink-0 ${done ? 'bg-emerald-500/30' : 'bg-border/30'}`} />
            )}
          </div>
        )
      })}
    </div>
  )
}

// ── Bento stat ───────────────────────────────────────────────────────────────

function BentoStat({ icon: Icon, label, value, accent }: {
  icon: React.ElementType; label: string; value: string; accent: string
}) {
  return (
    <motion.div variants={fadeUp} className="rounded-2xl bg-card p-5 flex flex-col justify-between border border-border shadow-sm dark:border-transparent dark:shadow-none">
      <div className={`h-8 w-8 rounded-xl flex items-center justify-center mb-4 ${accent}`}>
        <Icon className="h-4 w-4" />
      </div>
      <div>
        <p className="text-2xl font-bold text-foreground font-mono">{value}</p>
        <p className="text-xs text-muted-foreground mt-1">{label}</p>
      </div>
    </motion.div>
  )
}

// ── Severity chips ────────────────────────────────────────────────────────────

function SeverityChips({ counts }: { counts: { critical: number; warning: number; suggestion: number } }) {
  const crit = counts.critical
  const warn = counts.warning
  const sugg = counts.suggestion
  return (
    <div className="flex items-center gap-2 mt-4 flex-wrap">
      {crit > 0 && (
        <span className="text-xs font-bold px-2 py-0.5 rounded-md bg-red-50 border border-red-200 text-red-700 dark:bg-red-500/15 dark:border-red-500/20 dark:text-red-400">
          {crit} CRITICAL
        </span>
      )}
      {warn > 0 && (
        <span className="text-xs font-bold px-2 py-0.5 rounded-md bg-orange-50 border border-orange-200 text-orange-700 dark:bg-orange-500/15 dark:border-orange-500/20 dark:text-orange-400">
          {warn} WARNINGS
        </span>
      )}
      {sugg > 0 && (
        <span className="text-xs font-bold px-2 py-0.5 rounded-md bg-yellow-50 border border-yellow-200 text-yellow-700 dark:bg-yellow-500/15 dark:border-yellow-500/20 dark:text-yellow-500">
          {sugg} SUGGESTIONS
        </span>
      )}
    </div>
  )
}

// ── Review Row ────────────────────────────────────────────────────────────────

function ReviewRow({ review, repoId }: { review: ReviewSummary; repoId: string }) {
  return (
    <motion.div variants={fadeUp}>
      <Link
        to={`/repos/${repoId}/reviews/${review.pr_number}`}
        className="flex items-center gap-3 px-4 py-3 hover:bg-accent/30 transition-colors rounded-xl group"
      >
        <div className="h-7 w-7 rounded-lg bg-accent/50 flex items-center justify-center shrink-0">
          <GitPullRequest className="h-3.5 w-3.5 text-muted-foreground" />
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <span className="text-xs font-mono text-muted-foreground">#{review.pr_number}</span>
        </div>
        <p className="text-sm text-foreground flex-1 truncate">{review.pr_title}</p>
        <SeverityChips counts={review.severity_counts} />
        <span className="text-xs text-muted-foreground/50 shrink-0">
          {new Date(review.reviewed_at).toLocaleDateString()}
        </span>
        <ChevronRight className="h-4 w-4 text-muted-foreground/30 group-hover:text-muted-foreground transition-colors shrink-0" />
      </Link>
    </motion.div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

export default function RepoDetail() {
  const { repoId } = useParams<{ repoId: string }>()
  const queryClient = useQueryClient()
  const id = parseInt(repoId ?? '0', 10)

  const { data: repo, isLoading: repoLoading } = useQuery<Repository>({
    queryKey: ['repo', repoId],
    queryFn: () => api.repos.get(id),
    enabled: !!repoId,
  })

  const { data: scanState } = useQuery<ScanState | null>({
    queryKey: ['scanState', id],
    queryFn: () => api.repos.status(id),
    enabled: !!repoId,
  })

  // Open SSE connection when a scan is active — updates query cache and fires toasts
  const isActivelyScanning = scanState?.status === 'scanning' || scanState?.status === 'in_progress' || scanState?.status === 'pending'
  useScanProgress(isActivelyScanning ? id : undefined)

  const { data: stats, isLoading: statsLoading } = useQuery<RepoStats>({
    queryKey: ['stats', repoId],
    queryFn: () => api.repos.stats(id),
    enabled: !!repoId,
  })

  const { data: reviews } = useQuery<ReviewSummary[]>({
    queryKey: ['reviews', id],
    queryFn: () => api.reviews.list(id),
    enabled: scanState?.status === 'completed',
  })

  const triggerScan = useMutation({
    mutationFn: () => api.repos.scan(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['scanState', id] })
    },
  })

  if (repoLoading) {
    return (
      <div className="flex flex-col items-center justify-center py-32 gap-3">
        <Loader2 className="h-6 w-6 animate-spin text-primary" />
        <p className="text-sm text-muted-foreground">Loading repository...</p>
      </div>
    )
  }

  if (!repo) {
    return (
      <div className="flex flex-col items-center justify-center py-32 text-center gap-4 animate-fade-in">
        <p className="text-muted-foreground">Repository not found.</p>
        <Button asChild variant="outline"><Link to="/">← Back</Link></Button>
      </div>
    )
  }

  const isCompleted = scanState?.status === 'completed'
  const isScanning = scanState?.status === 'scanning' || scanState?.status === 'in_progress' || scanState?.status === 'pending'
  const hasStats = stats && stats.chunks_count > 0

  const progressPercent =
    scanState?.progress && scanState.progress.files_total > 0
      ? Math.round((scanState.progress.files_done / scanState.progress.files_total) * 100)
      : 0

  const [org, repoName] = repo.full_name.split('/')

  return (
    <motion.div className="space-y-6" initial="hidden" animate="show" variants={stagger}>
      {/* Header */}
      <motion.div variants={fadeUp} className="flex items-center gap-4">
        <Link
          to="/"
          className="p-2 rounded-lg hover:bg-accent/50 text-muted-foreground transition-colors shrink-0"
          aria-label="Back"
        >
          <ArrowLeft className="h-4 w-4" />
        </Link>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-3 flex-wrap">
            <h1 className="text-2xl font-bold text-foreground tracking-tight">
              <span className="text-muted-foreground font-normal">{org}/</span>{repoName}
            </h1>
            <StatusBadge status={scanState?.status} />
          </div>
          <p className="text-xs text-muted-foreground/50 font-mono truncate mt-0.5">{repo.clone_path}</p>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          {isCompleted && (
            <Button asChild variant="outline" size="sm" className="rounded-lg">
              <Link to={`/repos/${repoId}/chat`}>
                <MessageSquare className="h-3.5 w-3.5 mr-1.5" />
                Chat
              </Link>
            </Button>
          )}
          <Button
            variant="outline"
            size="sm"
            onClick={() => triggerScan.mutate()}
            disabled={isScanning || triggerScan.isPending}
            className="rounded-lg"
          >
            <RefreshCw className={`h-3.5 w-3.5 mr-1.5 ${isScanning ? 'animate-spin' : ''}`} />
            {isScanning ? 'Scanning...' : 'Re-scan'}
          </Button>
        </div>
      </motion.div>

      {/* Pipeline stages */}
      {scanState && (
        <motion.div variants={fadeUp}>
          <PipelineBar status={scanState.status} />
        </motion.div>
      )}

      {/* Active scan progress */}
      {isScanning && scanState && (
        <motion.div variants={fadeUp} className="rounded-2xl bg-blue-500/5 border border-blue-500/10 p-5 space-y-3">
          <div className="flex items-center gap-2.5">
            <div className="h-7 w-7 rounded-lg bg-blue-500/15 flex items-center justify-center">
              <RefreshCw className="h-3.5 w-3.5 text-blue-400 animate-spin" />
            </div>
            <div>
              <p className="text-sm font-medium text-foreground">{scanState.progress?.stage || 'Scanning in progress...'}</p>
              {scanState.progress?.current_file && (
                <p className="text-xs text-muted-foreground/60 font-mono truncate">{scanState.progress.current_file}</p>
              )}
            </div>
          </div>
          <div className="h-1.5 rounded-full bg-blue-500/10 overflow-hidden">
            <div
              className="h-full rounded-full bg-blue-400 transition-all duration-500"
              style={{ width: progressPercent > 0 ? `${progressPercent}%` : '15%' }}
            />
          </div>
          {scanState.progress && scanState.progress.files_total > 0 && (
            <p className="text-xs text-muted-foreground">
              {scanState.progress.files_done.toLocaleString()} / {scanState.progress.files_total.toLocaleString()} files indexed
              {progressPercent > 0 && <span className="ml-2 text-blue-400 font-medium">{progressPercent}%</span>}
            </p>
          )}
        </motion.div>
      )}

      {/* Not indexed CTA */}
      {!scanState && !isScanning && (
        <motion.div variants={fadeUp} className="rounded-2xl bg-card p-10 text-center border border-border shadow-sm dark:border-transparent dark:shadow-none">
          <div className="relative mx-auto mb-5 w-fit">
            <div className="absolute inset-0 rounded-2xl bg-primary/15 blur-xl scale-150" />
            <div className="relative h-16 w-16 rounded-2xl bg-primary/10 flex items-center justify-center">
              <Layers className="h-8 w-8 text-primary" />
            </div>
          </div>
          <h2 className="text-lg font-semibold mb-2">This repository hasn't been indexed yet</h2>
          <p className="text-muted-foreground text-sm mb-6 max-w-sm mx-auto">
            Run the initial scan to enable AI-powered code reviews and Q&amp;A.
          </p>
          <Button onClick={() => triggerScan.mutate()} disabled={triggerScan.isPending} size="lg" className="rounded-xl px-8">
            {triggerScan.isPending
              ? <><Loader2 className="h-4 w-4 mr-2 animate-spin" />Starting...</>
              : <><RefreshCw className="h-4 w-4 mr-2" />Run Initial Scan</>}
          </Button>
        </motion.div>
      )}

      {/* Stats */}
      {isCompleted && statsLoading && (
        <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
          {[1, 2, 3, 4].map(i => <div key={i} className="rounded-2xl bg-card p-5 h-28 animate-shimmer border border-border shadow-sm dark:border-transparent dark:shadow-none" />)}
        </div>
      )}
      {isCompleted && hasStats && (
        <motion.div variants={stagger} className="grid grid-cols-2 md:grid-cols-4 gap-3">
          <BentoStat icon={FileCode} label="Files indexed" value={stats.files_count.toLocaleString()} accent="bg-sky-500/10 text-sky-400" />
          <BentoStat icon={Layers} label="Chunks" value={stats.chunks_count.toLocaleString()} accent="bg-violet-500/10 text-violet-400" />
          <BentoStat
            icon={Hash}
            label="Last SHA"
            value={stats.last_indexed_sha ? stats.last_indexed_sha.slice(0, 7) : '—'}
            accent="bg-amber-500/10 text-amber-400"
          />
          <BentoStat
            icon={CalendarDays}
            label="Last scan"
            value={stats.last_scan_date ? new Date(stats.last_scan_date).toLocaleDateString() : '—'}
            accent="bg-emerald-500/10 text-emerald-400"
          />
        </motion.div>
      )}

      {/* Recent Reviews */}
      {isCompleted && (
        <motion.div variants={fadeUp} className="space-y-3">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-semibold text-muted-foreground uppercase tracking-wider">Recent Reviews</h2>
            <Link to={`/repos/${repoId}/reviews`} className="text-xs text-primary hover:underline">View all →</Link>
          </div>
          <div className="rounded-2xl bg-card overflow-hidden border border-border shadow-sm dark:border-transparent dark:shadow-none">
            {reviews && reviews.length > 0 ? (
              <motion.div variants={stagger} className="divide-y divide-border/20">
                {reviews.slice(0, 3).map(r => (
                  <ReviewRow key={r.id} review={r} repoId={repoId!} />
                ))}
              </motion.div>
            ) : (
              <div className="py-8 text-center text-sm text-muted-foreground">
                No reviews yet — comment{' '}
                <code className="font-mono text-xs bg-accent/50 px-1.5 py-0.5 rounded">/review</code>{' '}
                on a GitHub PR to get started.
              </div>
            )}
          </div>
        </motion.div>
      )}

      {/* Action cards */}
      {isCompleted && (
        <motion.div variants={stagger} className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <motion.div variants={fadeUp}>
            <Link
              to={`/repos/${repoId}/chat`}
              className="block rounded-2xl bg-card p-6 border border-border shadow-sm hover:shadow-lg dark:border-transparent dark:shadow-none dark:hover:shadow-primary/5 transition-all duration-200 group h-full"
            >
              <div className="flex items-center gap-3 mb-3">
                <div className="h-9 w-9 rounded-xl bg-primary/10 flex items-center justify-center shrink-0 group-hover:bg-primary/15 transition-colors">
                  <MessageSquare className="h-4.5 w-4.5 text-primary" />
                </div>
                <div>
                  <h3 className="font-semibold text-foreground">Explore with AI</h3>
                  <p className="text-xs text-muted-foreground">Chat about your codebase</p>
                </div>
              </div>
              <p className="text-sm text-muted-foreground mb-4">
                Ask about architecture, patterns, or functionality. Use{' '}
                <code className="font-mono text-xs bg-accent/50 px-1.5 py-0.5 rounded text-foreground">/explain &lt;path&gt;</code>{' '}
                for file context.
              </p>
              <span className="inline-flex items-center gap-1.5 text-sm font-medium text-primary">
                Start Chat <ChevronRight className="h-4 w-4" />
              </span>
            </Link>
          </motion.div>

          <motion.div variants={fadeUp}>
            <Link
              to={`/repos/${repoId}/reviews`}
              className="block rounded-2xl bg-card p-6 border border-border shadow-sm hover:shadow-lg dark:border-transparent dark:shadow-none dark:hover:shadow-primary/5 transition-all duration-200 group h-full"
            >
              <div className="flex items-center gap-3 mb-3">
                <div className="h-9 w-9 rounded-xl bg-blue-500/10 flex items-center justify-center shrink-0 group-hover:bg-blue-500/15 transition-colors">
                  <GitPullRequest className="h-4.5 w-4.5 text-blue-400" />
                </div>
                <div>
                  <h3 className="font-semibold text-foreground">Code Reviews</h3>
                  <p className="text-xs text-muted-foreground">Browse review history</p>
                </div>
              </div>
              <p className="text-sm text-muted-foreground mb-4">
                View all AI-generated reviews for this repository. Filter by severity and track findings over time.
              </p>
              <span className="inline-flex items-center gap-1.5 text-sm font-medium text-blue-400">
                View Reviews <ChevronRight className="h-4 w-4" />
              </span>
            </Link>
          </motion.div>
        </motion.div>
      )}
    </motion.div>
  )
}
