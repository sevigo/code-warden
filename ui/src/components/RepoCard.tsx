import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { GitBranch, RefreshCw, CheckCircle2, XCircle, Clock, FolderOpen } from 'lucide-react'
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

function RepoIcon({ name }: { name: string }) {
  const initials = name.split('/')[0]?.slice(0, 2).toUpperCase() ?? '??'
  return (
    <div className="h-11 w-11 rounded-xl bg-zinc-800 border border-zinc-700 flex items-center justify-center shrink-0">
      <span className="text-xs font-bold text-zinc-300 font-mono">{initials}</span>
    </div>
  )
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

  const card = (
    <div className={`group rounded-xl border bg-card transition-all duration-200 ${
      isCompleted
        ? 'border-zinc-800 hover:border-primary/40 hover:shadow-lg hover:shadow-primary/5 cursor-pointer'
        : 'border-zinc-800'
    }`}>
      <div className="p-5">
        <div className="flex items-start gap-4">
          <RepoIcon name={repo.full_name} />

          <div className="flex-1 min-w-0">
            {/* Title row */}
            <div className="flex items-center gap-2 mb-1 flex-wrap">
              <div className="flex items-center gap-1 min-w-0">
                <span className="text-sm text-zinc-500 shrink-0">{org}/</span>
                <span className="font-semibold text-foreground text-base truncate">{repoName}</span>
              </div>
              <StatusBadge status={scanState?.status} />
            </div>

            {/* Path */}
            <div className="flex items-center gap-1.5 text-xs text-zinc-500 mb-1">
              <FolderOpen className="h-3 w-3 shrink-0" />
              <code className="truncate font-mono">{repo.clone_path}</code>
            </div>

            {/* SHA */}
            {shortSHA && (
              <div className="flex items-center gap-1.5 text-xs text-zinc-600">
                <GitBranch className="h-3 w-3 shrink-0" />
                <code className="font-mono">{shortSHA}</code>
              </div>
            )}
          </div>

          {/* Actions */}
          <div className="flex items-center gap-2 shrink-0 mt-0.5">
            {(noScan || isFailed) && (
              <Button
                size="sm"
                onClick={(e) => {
                  e.preventDefault()
                  e.stopPropagation()
                  onScan?.()
                }}
              >
                Start Scan
              </Button>
            )}

            {isScanning && (
              <Button size="sm" disabled variant="secondary" className="gap-1.5">
                <RefreshCw className="h-3 w-3 animate-spin" />
                Scanning...
              </Button>
            )}

            {isCompleted && (
              <Button size="sm" variant="outline" className="gap-1.5 group-hover:bg-primary group-hover:text-primary-foreground group-hover:border-primary transition-colors">
                Explore →
              </Button>
            )}
          </div>
        </div>

        {/* Scan progress */}
        {isScanning && (
          <div className="mt-4 pt-4 border-t border-zinc-800">
            <div className="flex items-center gap-2 text-xs text-blue-400 mb-2">
              <RefreshCw className="h-3 w-3 animate-spin shrink-0" />
              <span className="truncate">{scanState?.progress?.stage || 'Scanning...'}</span>
              {scanState?.progress?.current_file && (
                <span className="truncate text-zinc-500 hidden sm:block">— {scanState.progress.current_file}</span>
              )}
            </div>
            <Progress value={progressPercent > 0 ? progressPercent : undefined} className="h-1" />
            {scanState?.progress && scanState.progress.files_total > 0 && (
              <p className="text-xs text-zinc-600 mt-1.5">
                {scanState.progress.files_done.toLocaleString()} / {scanState.progress.files_total.toLocaleString()} files
              </p>
            )}
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
