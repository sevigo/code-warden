import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useParams, Link } from 'react-router-dom'
import { motion } from 'framer-motion'
import {
  ArrowLeft,
  MessageSquare,
  GitBranch,
  RefreshCw,
  Layers,
  FileCode,
  Hash,
  CalendarDays,
  Loader2,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import StatusBadge from '@/components/StatusBadge'
import { api } from '@/lib/api'
import type { Repository, ScanState, RepoStats } from '@/lib/api'

const stagger = {
  hidden: {},
  show: { transition: { staggerChildren: 0.06 } },
}

const fadeUp = {
  hidden: { opacity: 0, y: 12 },
  show: { opacity: 1, y: 0, transition: { duration: 0.35 } },
}

function BentoStat({ icon: Icon, label, value, accent, large }: {
  icon: React.ElementType; label: string; value: string; accent: string; large?: boolean
}) {
  return (
    <motion.div
      variants={fadeUp}
      className={`rounded-2xl bg-card p-5 flex flex-col justify-between ${large ? 'md:col-span-2 md:row-span-1' : ''}`}
    >
      <div className={`h-9 w-9 rounded-xl flex items-center justify-center mb-4 ${accent}`}>
        <Icon className="h-4.5 w-4.5" />
      </div>
      <div>
        <p className={`font-bold text-foreground font-mono ${large ? 'text-3xl' : 'text-2xl'}`}>{value}</p>
        <p className="text-xs text-muted-foreground mt-1">{label}</p>
      </div>
    </motion.div>
  )
}

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
    refetchInterval: (query) => {
      const data = query.state.data
      if (data && (data.status === 'scanning' || data.status === 'in_progress' || data.status === 'pending')) return 2000
      return false
    },
    enabled: !!repoId,
  })

  const { data: stats, isLoading: statsLoading } = useQuery<RepoStats>({
    queryKey: ['stats', repoId],
    queryFn: () => api.repos.stats(parseInt(repoId!)),
    enabled: !!repoId,
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
        <Button asChild variant="outline">
          <Link to="/">← Back</Link>
        </Button>
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
    <motion.div
      className="space-y-8"
      initial="hidden"
      animate="show"
      variants={stagger}
    >
      {/* Header */}
      <motion.div variants={fadeUp} className="flex items-center gap-4">
        <Link
          to="/"
          className="p-2 rounded-lg hover:bg-accent/50 text-muted-foreground transition-colors shrink-0"
          aria-label="Back"
        >
          <ArrowLeft className="h-5 w-5" />
        </Link>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-3 flex-wrap">
            <h1 className="text-2xl font-bold text-foreground">
              <span className="text-muted-foreground font-normal">{org}/</span>{repoName}
            </h1>
            <StatusBadge status={scanState?.status} />
          </div>
          <p className="text-xs text-muted-foreground/60 font-mono truncate mt-1">{repo.clone_path}</p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => triggerScan.mutate()}
          disabled={isScanning || triggerScan.isPending}
          className="shrink-0 rounded-lg"
        >
          <RefreshCw className={`h-3.5 w-3.5 mr-1.5 ${isScanning ? 'animate-spin' : ''}`} />
          {isScanning ? 'Scanning...' : 'Re-scan'}
        </Button>
      </motion.div>

      {/* Active scan progress */}
      {isScanning && scanState && (
        <motion.div variants={fadeUp} className="rounded-2xl bg-blue-500/5 p-6 space-y-4">
          <div className="flex items-center gap-2.5">
            <div className="h-8 w-8 rounded-lg bg-blue-500/15 flex items-center justify-center animate-glow-pulse">
              <RefreshCw className="h-4 w-4 text-blue-400 animate-spin" />
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
        <motion.div variants={fadeUp} className="rounded-2xl bg-card p-10 text-center">
          <div className="relative mx-auto mb-5 w-fit">
            <div className="absolute inset-0 rounded-2xl bg-primary/15 blur-xl scale-150" />
            <div className="relative h-16 w-16 rounded-2xl bg-primary/10 flex items-center justify-center">
              <Layers className="h-8 w-8 text-primary" />
            </div>
          </div>
          <h2 className="text-lg font-semibold mb-2">This repository hasn't been indexed yet</h2>
          <p className="text-muted-foreground text-sm mb-6 max-w-sm mx-auto">
            Run the initial scan to enable AI-powered exploration and Q&amp;A.
          </p>
          <Button onClick={() => triggerScan.mutate()} disabled={triggerScan.isPending} size="lg" className="rounded-xl px-8">
            {triggerScan.isPending ? (
              <><Loader2 className="h-4 w-4 mr-2 animate-spin" />Starting...</>
            ) : (
              <><RefreshCw className="h-4 w-4 mr-2" />Run Initial Scan</>
            )}
          </Button>
        </motion.div>
      )}

      {/* Bento stats */}
      {isCompleted && statsLoading && (
        <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
          {[1, 2, 3, 4].map((i) => (
            <div key={i} className="rounded-2xl bg-card p-5 h-28 animate-shimmer" />
          ))}
        </div>
      )}
      {isCompleted && hasStats && (
        <motion.div variants={stagger} className="grid grid-cols-2 md:grid-cols-4 gap-3">
          <BentoStat icon={FileCode} label="Files indexed" value={stats.files_count.toLocaleString()} accent="bg-sky-500/10 text-sky-400" large />
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

      {/* Action cards */}
      {isCompleted && (
        <motion.div variants={stagger} className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {/* Explore with AI */}
          <motion.div variants={fadeUp}>
            <Link
              to={`/repos/${repoId}/chat`}
              className="block rounded-2xl bg-card p-6 hover:shadow-lg hover:shadow-primary/5 transition-all duration-200 group h-full"
            >
              <div className="flex items-center gap-3 mb-4">
                <div className="h-10 w-10 rounded-xl bg-primary/10 flex items-center justify-center shrink-0 group-hover:bg-primary/15 transition-colors">
                  <MessageSquare className="h-5 w-5 text-primary" />
                </div>
                <div>
                  <h3 className="font-semibold text-foreground">Explore with AI</h3>
                  <p className="text-xs text-muted-foreground">Chat about your codebase</p>
                </div>
              </div>
              <p className="text-sm text-muted-foreground mb-5">
                Ask about architecture, patterns, functionality. Use{' '}
                <code className="font-mono text-xs bg-accent/50 px-1.5 py-0.5 rounded text-foreground">/explain &lt;path&gt;</code>{' '}
                for file context.
              </p>
              <span className="inline-flex items-center gap-2 text-sm font-medium text-primary group-hover:gap-2.5 transition-all">
                Start Chat <ArrowLeft className="h-4 w-4 rotate-180" />
              </span>
            </Link>
          </motion.div>

          {/* Repository info */}
          <motion.div variants={fadeUp} className="rounded-2xl bg-card p-6">
            <div className="flex items-center gap-3 mb-4">
              <div className="h-10 w-10 rounded-xl bg-accent/50 flex items-center justify-center shrink-0">
                <GitBranch className="h-5 w-5 text-muted-foreground" />
              </div>
              <div>
                <h3 className="font-semibold text-foreground">Repository Info</h3>
                <p className="text-xs text-muted-foreground">Index details</p>
              </div>
            </div>
            <div className="space-y-0 text-sm">
              <div className="flex justify-between items-center py-3 border-b border-border/30">
                <span className="text-muted-foreground">Full name</span>
                <span className="font-medium font-mono text-xs text-foreground">{repo.full_name}</span>
              </div>
              <div className="flex justify-between items-center py-3 border-b border-border/30 gap-4">
                <span className="text-muted-foreground shrink-0">Path</span>
                <code className="font-mono text-xs text-muted-foreground truncate">{repo.clone_path}</code>
              </div>
              {repo.last_indexed_sha && (
                <div className="flex justify-between items-center py-3">
                  <span className="text-muted-foreground">Indexed SHA</span>
                  <code className="font-mono text-xs text-foreground">{repo.last_indexed_sha.slice(0, 12)}</code>
                </div>
              )}
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => triggerScan.mutate()}
              disabled={triggerScan.isPending}
              className="w-full mt-4 rounded-lg"
            >
              {triggerScan.isPending ? (
                <><Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" />Starting...</>
              ) : (
                <><RefreshCw className="h-3.5 w-3.5 mr-1.5" />Re-index</>
              )}
            </Button>
          </motion.div>
        </motion.div>
      )}
    </motion.div>
  )
}
