import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useParams, Link } from 'react-router-dom'
import {
  ArrowLeft,
  MessageSquare,
  GitBranch,
  RefreshCw,
  CheckCircle2,
  XCircle,
  Clock,
  Layers,
  FileCode,
  Hash,
  CalendarDays,
} from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import type { Repository, ScanState, RepoStats } from '@/lib/api'

function StatusBadge({ status }: { status: ScanState['status'] | null | undefined }) {
  if (!status) {
    return (
      <Badge variant="secondary" className="gap-1.5">
        <Clock className="h-3 w-3" />
        Not Indexed
      </Badge>
    )
  }
  switch (status) {
    case 'scanning':
    case 'in_progress':
    case 'pending':
      return (
        <Badge className="gap-1.5 bg-blue-500/20 text-blue-400 border-blue-500/30 hover:bg-blue-500/20">
          <RefreshCw className="h-3 w-3 animate-spin" />
          Indexing
        </Badge>
      )
    case 'completed':
      return (
        <Badge className="gap-1.5 bg-emerald-500/20 text-emerald-400 border-emerald-500/30 hover:bg-emerald-500/20">
          <CheckCircle2 className="h-3 w-3" />
          Ready
        </Badge>
      )
    case 'failed':
      return (
        <Badge variant="destructive" className="gap-1.5">
          <XCircle className="h-3 w-3" />
          Failed
        </Badge>
      )
    default:
      return (
        <Badge variant="secondary" className="gap-1.5">
          <Clock className="h-3 w-3" />
          Not Indexed
        </Badge>
      )
  }
}

function StatCard({ icon: Icon, label, value }: { icon: React.ElementType; label: string; value: string }) {
  return (
    <div className="rounded-xl border border-zinc-800 bg-card p-4">
      <div className="flex items-center gap-2 text-xs text-muted-foreground mb-2">
        <Icon className="h-3.5 w-3.5" />
        {label}
      </div>
      <p className="text-xl font-semibold text-foreground font-mono">{value}</p>
    </div>
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

  const { data: stats } = useQuery<RepoStats>({
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
      <div className="space-y-6 animate-pulse">
        <div className="h-8 bg-card rounded-lg w-72" />
        <div className="h-40 bg-card rounded-xl border border-zinc-800" />
      </div>
    )
  }

  if (!repo) {
    return (
      <div>
        <p className="text-muted-foreground">Repository not found.</p>
        <Link to="/" className="text-primary hover:underline text-sm mt-2 block">
          ← Back to repositories
        </Link>
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
    <div className="space-y-7">
      {/* Header */}
      <div className="flex items-center gap-4">
        <Link
          to="/"
          className="p-2 rounded-lg hover:bg-muted text-muted-foreground transition-colors shrink-0"
        >
          <ArrowLeft className="h-5 w-5" />
        </Link>
        <div className="flex items-center gap-3 flex-1 min-w-0">
          <div className="h-10 w-10 rounded-xl bg-zinc-800 border border-zinc-700 flex items-center justify-center shrink-0">
            <span className="text-xs font-bold text-zinc-300 font-mono">
              {org?.slice(0, 2).toUpperCase()}
            </span>
          </div>
          <div className="min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <h1 className="text-xl font-bold text-foreground">
                <span className="text-muted-foreground font-normal">{org}/</span>{repoName}
              </h1>
              <StatusBadge status={scanState?.status} />
            </div>
            <p className="text-xs text-muted-foreground font-mono truncate mt-0.5">{repo.clone_path}</p>
          </div>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => triggerScan.mutate()}
          disabled={isScanning || triggerScan.isPending}
          className="shrink-0"
        >
          <RefreshCw className={`h-3.5 w-3.5 mr-1.5 ${isScanning ? 'animate-spin' : ''}`} />
          {isScanning ? 'Scanning...' : 'Re-scan'}
        </Button>
      </div>

      {/* Scan progress */}
      {isScanning && scanState && (
        <div className="rounded-xl border border-blue-500/20 bg-blue-500/5 p-5 space-y-3">
          <div className="flex items-center gap-2 text-sm text-blue-300">
            <RefreshCw className="h-4 w-4 animate-spin shrink-0" />
            <span className="font-medium">{scanState.progress?.stage || 'Scanning in progress...'}</span>
          </div>
          {scanState.progress?.current_file && (
            <p className="text-xs text-blue-400/60 font-mono truncate pl-6">
              {scanState.progress.current_file}
            </p>
          )}
          <Progress value={progressPercent > 0 ? progressPercent : undefined} className="h-1.5" />
          {scanState.progress && scanState.progress.files_total > 0 && (
            <p className="text-xs text-blue-400/60 pl-6">
              {scanState.progress.files_done.toLocaleString()} / {scanState.progress.files_total.toLocaleString()} files
              {progressPercent > 0 && <span className="ml-2 text-blue-300">{progressPercent}%</span>}
            </p>
          )}
        </div>
      )}

      {/* Not indexed CTA */}
      {!scanState && !isScanning && (
        <div className="rounded-xl border border-dashed border-zinc-700 bg-zinc-900/50 p-10 text-center">
          <div className="h-14 w-14 rounded-2xl bg-primary/10 flex items-center justify-center mx-auto mb-4">
            <Layers className="h-7 w-7 text-primary" />
          </div>
          <h2 className="text-lg font-semibold mb-2">This repository hasn't been indexed yet</h2>
          <p className="text-muted-foreground text-sm mb-6 max-w-sm mx-auto">
            Run the initial scan to index the codebase and enable AI-powered exploration and Q&amp;A.
          </p>
          <Button onClick={() => triggerScan.mutate()} disabled={triggerScan.isPending} size="lg">
            <RefreshCw className="h-4 w-4 mr-2" />
            Run Initial Scan
          </Button>
        </div>
      )}

      {/* Stats row */}
      {isCompleted && hasStats && (
        <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
          <StatCard icon={Layers} label="Chunks" value={stats.chunks_count.toLocaleString()} />
          <StatCard icon={FileCode} label="Files indexed" value={stats.files_count.toLocaleString()} />
          <StatCard
            icon={Hash}
            label="Last SHA"
            value={stats.last_indexed_sha ? stats.last_indexed_sha.slice(0, 7) : '—'}
          />
          <StatCard
            icon={CalendarDays}
            label="Last scan"
            value={stats.last_scan_date ? new Date(stats.last_scan_date).toLocaleDateString() : '—'}
          />
        </div>
      )}

      {/* Action cards */}
      {isCompleted && (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {/* Explore with AI */}
          <div className="rounded-xl border border-zinc-800 bg-card p-6 flex flex-col gap-4 hover:border-primary/40 transition-colors">
            <div className="flex items-center gap-3">
              <div className="h-10 w-10 rounded-xl bg-primary/10 flex items-center justify-center shrink-0">
                <MessageSquare className="h-5 w-5 text-primary" />
              </div>
              <div>
                <h3 className="font-semibold text-foreground">Explore with AI</h3>
                <p className="text-xs text-muted-foreground">Chat about your codebase</p>
              </div>
            </div>
            <p className="text-sm text-muted-foreground">
              Ask questions about architecture, patterns, and functionality. Use{' '}
              <code className="font-mono text-xs bg-muted px-1.5 py-0.5 rounded">/explain &lt;path&gt;</code>{' '}
              to get architectural context for any file or directory.
            </p>
            <Button asChild className="w-full mt-auto">
              <Link to={`/repos/${repoId}/chat`}>
                <MessageSquare className="h-4 w-4 mr-2" />
                Start Chat
              </Link>
            </Button>
          </div>

          {/* Index info */}
          <div className="rounded-xl border border-zinc-800 bg-card p-6 flex flex-col gap-4">
            <div className="flex items-center gap-3">
              <div className="h-10 w-10 rounded-xl bg-muted flex items-center justify-center shrink-0">
                <GitBranch className="h-5 w-5 text-muted-foreground" />
              </div>
              <div>
                <h3 className="font-semibold text-foreground">Repository Info</h3>
                <p className="text-xs text-muted-foreground">Index details</p>
              </div>
            </div>
            <div className="space-y-3 text-sm flex-1">
              <div className="flex justify-between items-center py-2 border-b border-zinc-800">
                <span className="text-muted-foreground">Full name</span>
                <span className="font-medium font-mono text-xs">{repo.full_name}</span>
              </div>
              <div className="flex justify-between items-center py-2 border-b border-zinc-800">
                <span className="text-muted-foreground">Path</span>
                <code className="font-mono text-xs text-zinc-400 max-w-[200px] truncate text-right">{repo.clone_path}</code>
              </div>
              {repo.last_indexed_sha && (
                <div className="flex justify-between items-center py-2">
                  <span className="text-muted-foreground">Indexed SHA</span>
                  <code className="font-mono text-xs">{repo.last_indexed_sha.slice(0, 12)}</code>
                </div>
              )}
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => triggerScan.mutate()}
              disabled={triggerScan.isPending}
              className="w-full"
            >
              <RefreshCw className="h-3.5 w-3.5 mr-1.5" />
              Re-index Repository
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
