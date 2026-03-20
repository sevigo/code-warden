import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { GitBranch, RefreshCw, CheckCircle2, XCircle, Clock, FolderOpen, ArrowRight } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import type { Repository, ScanState } from '@/lib/api'

interface RepoCardProps {
  repo: Repository
  onScan?: () => void
}

function StatusBadge({ status }: { status: ScanState['status'] | null | undefined }) {
  if (!status) {
    return (
      <Badge variant="secondary" className="gap-1.5 text-xs">
        <Clock className="h-3 w-3" />
        Not Indexed
      </Badge>
    )
  }
  switch (status) {
    case 'scanning':
    case 'pending':
      return (
        <Badge className="gap-1.5 text-xs bg-blue-500/20 text-blue-400 border-blue-500/30 hover:bg-blue-500/20">
          <RefreshCw className="h-3 w-3 animate-spin" />
          Indexing
        </Badge>
      )
    case 'completed':
      return (
        <Badge className="gap-1.5 text-xs bg-emerald-500/20 text-emerald-400 border-emerald-500/30 hover:bg-emerald-500/20">
          <CheckCircle2 className="h-3 w-3" />
          Ready
        </Badge>
      )
    case 'failed':
      return (
        <Badge variant="destructive" className="gap-1.5 text-xs">
          <XCircle className="h-3 w-3" />
          Failed
        </Badge>
      )
    default:
      return (
        <Badge variant="secondary" className="gap-1.5 text-xs">
          <Clock className="h-3 w-3" />
          Not Indexed
        </Badge>
      )
  }
}

export default function RepoCard({ repo, onScan }: RepoCardProps) {
  const { data: scanState } = useQuery<ScanState | null>({
    queryKey: ['scanState', repo.id],
    queryFn: () => api.repos.status(repo.id),
    refetchInterval: (query) =>
      query.state.data?.status === 'scanning' || query.state.data?.status === 'pending'
        ? 2000
        : false,
  })

  const isScanning = scanState?.status === 'scanning' || scanState?.status === 'pending'
  const isCompleted = scanState?.status === 'completed'
  const isFailed = scanState?.status === 'failed'
  const noScan = !scanState

  const progressPercent =
    scanState?.progress && scanState.progress.files_total > 0
      ? Math.round((scanState.progress.files_done / scanState.progress.files_total) * 100)
      : 0

  const shortSHA = repo.last_indexed_sha ? repo.last_indexed_sha.slice(0, 7) : null
  const [org, repoName] = repo.full_name.split('/')
  const orgInitials = (org ?? '??').slice(0, 2).toUpperCase()

  // Status accent color for top border
  const accentClass = isCompleted
    ? 'border-t-emerald-500/60'
    : isScanning
    ? 'border-t-blue-500/60'
    : isFailed
    ? 'border-t-red-500/60'
    : 'border-t-zinc-700'

  const card = (
    <div
      className={`group relative flex flex-col rounded-xl border border-zinc-800 border-t-2 ${accentClass} bg-card transition-all duration-200 ${
        isCompleted ? 'hover:border-zinc-700 hover:shadow-xl hover:shadow-black/30 hover:-translate-y-0.5 cursor-pointer' : ''
      }`}
    >
      {/* Card body */}
      <div className="flex flex-col flex-1 p-5 gap-4">
        {/* Top row: avatar + status */}
        <div className="flex items-start justify-between gap-2">
          <div className="h-10 w-10 rounded-xl bg-zinc-800 border border-zinc-700 flex items-center justify-center shrink-0">
            <span className="text-xs font-bold text-zinc-300 font-mono">{orgInitials}</span>
          </div>
          <StatusBadge status={scanState?.status} />
        </div>

        {/* Repo name */}
        <div className="min-w-0">
          <p className="text-xs text-zinc-500 mb-0.5 font-mono">{org}/</p>
          <h3 className="font-bold text-foreground text-lg leading-tight truncate">{repoName}</h3>
        </div>

        {/* Meta */}
        <div className="flex flex-col gap-1.5 flex-1">
          <div className="flex items-center gap-1.5 text-xs text-zinc-500">
            <FolderOpen className="h-3 w-3 shrink-0" />
            <span className="font-mono truncate">{repo.clone_path}</span>
          </div>
          {shortSHA && (
            <div className="flex items-center gap-1.5 text-xs text-zinc-600">
              <GitBranch className="h-3 w-3 shrink-0" />
              <code className="font-mono">{shortSHA}</code>
            </div>
          )}
        </div>

        {/* Scan progress */}
        {isScanning && (
          <div className="space-y-2">
            <div className="flex items-center gap-1.5 text-xs text-blue-400">
              <RefreshCw className="h-3 w-3 animate-spin shrink-0" />
              <span className="truncate">{scanState?.progress?.stage || 'Scanning...'}</span>
            </div>
            <Progress value={progressPercent > 0 ? progressPercent : undefined} className="h-1" />
            {scanState?.progress && scanState.progress.files_total > 0 && (
              <p className="text-xs text-zinc-600">
                {scanState.progress.files_done.toLocaleString()} / {scanState.progress.files_total.toLocaleString()} files
              </p>
            )}
          </div>
        )}
      </div>

      {/* Footer action */}
      <div className="border-t border-zinc-800 px-5 py-3">
        {(noScan || isFailed) && (
          <Button
            size="sm"
            className="w-full"
            onClick={(e) => {
              e.preventDefault()
              e.stopPropagation()
              onScan?.()
            }}
          >
            {isFailed ? 'Retry Scan' : 'Start Scan'}
          </Button>
        )}
        {isScanning && (
          <div className="flex items-center justify-center gap-1.5 text-xs text-blue-400 py-1">
            <RefreshCw className="h-3 w-3 animate-spin" />
            Indexing in progress...
          </div>
        )}
        {isCompleted && (
          <div className="flex items-center justify-between text-sm text-zinc-400 group-hover:text-zinc-200 transition-colors">
            <span>Explore codebase</span>
            <ArrowRight className="h-4 w-4 group-hover:translate-x-0.5 transition-transform" />
          </div>
        )}
      </div>
    </div>
  )

  if (isCompleted) {
    return (
      <Link to={`/repos/${repo.id}`} className="block">
        {card}
      </Link>
    )
  }

  return card
}
