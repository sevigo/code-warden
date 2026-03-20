import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { GitBranch, RefreshCw, FolderOpen, ArrowRight } from 'lucide-react'
import { Progress } from '@/components/ui/progress'
import { Button } from '@/components/ui/button'
import StatusBadge from '@/components/StatusBadge'
import { api } from '@/lib/api'
import type { Repository, ScanState } from '@/lib/api'

interface RepoCardProps {
  repo: Repository
  onScan?: () => void
}

/** Deterministic hue from name for avatar gradient */
function nameHue(name: string): number {
  let hash = 0
  for (let i = 0; i < name.length; i++) hash = name.charCodeAt(i) + ((hash << 5) - hash)
  return Math.abs(hash) % 360
}

export default function RepoCard({ repo, onScan }: RepoCardProps) {
  const { data: scanState } = useQuery<ScanState | null>({
    queryKey: ['scanState', repo.id],
    queryFn: () => api.repos.status(repo.id),
    refetchInterval: (query) => {
      const s = query.state.data?.status
      return s === 'scanning' || s === 'in_progress' || s === 'pending' ? 2000 : false
    },
  })

  const isScanning = scanState?.status === 'scanning' || scanState?.status === 'in_progress' || scanState?.status === 'pending'
  const isCompleted = scanState?.status === 'completed'
  const isFailed = scanState?.status === 'failed'
  const noScan = !scanState

  const progressPercent =
    scanState?.progress && scanState.progress.files_total > 0
      ? Math.round((scanState.progress.files_done / scanState.progress.files_total) * 100)
      : 0

  const shortSHA = repo.last_indexed_sha ? repo.last_indexed_sha.slice(0, 7) : null
  const [org, repoName] = repo.full_name.split('/')
  const hue = nameHue(repo.full_name)

  // Status accent color for top border
  const accentClass = isCompleted
    ? 'border-t-emerald-500/60'
    : isScanning
    ? 'border-t-blue-500/60'
    : isFailed
    ? 'border-t-red-500/60'
    : 'border-t-zinc-700/60'

  const card = (
    <div
      className={`group relative flex flex-col rounded-xl border border-zinc-800/80 border-t-2 ${accentClass} bg-card transition-all duration-200 hover:border-zinc-700 hover:shadow-xl hover:shadow-black/20 hover:-translate-y-0.5 ${
        isScanning ? 'animate-pulse-glow' : ''
      } ${isCompleted ? 'cursor-pointer' : ''}`}
    >
      {/* Card body */}
      <div className="flex flex-col flex-1 p-5 gap-4">
        {/* Top row: avatar + status */}
        <div className="flex items-start justify-between gap-2">
          <div
            className="h-10 w-10 rounded-xl flex items-center justify-center shrink-0 border border-white/10"
            style={{
              background: `linear-gradient(135deg, hsl(${hue} 50% 25%), hsl(${hue + 40} 40% 18%))`,
            }}
          >
            <span className="text-xs font-bold text-white/90 font-mono">
              {(org ?? '??').slice(0, 2).toUpperCase()}
            </span>
          </div>
          <StatusBadge status={scanState?.status} size="sm" />
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
            <Progress value={progressPercent > 0 ? progressPercent : undefined} className="h-1.5" />
            {scanState?.progress && scanState.progress.files_total > 0 && (
              <p className="text-xs text-zinc-500">
                {scanState.progress.files_done.toLocaleString()} / {scanState.progress.files_total.toLocaleString()} files
                {progressPercent > 0 && <span className="ml-2 text-blue-400 font-medium">{progressPercent}%</span>}
              </p>
            )}
          </div>
        )}
      </div>

      {/* Footer action */}
      <div className="border-t border-zinc-800/60 px-5 py-3 bg-zinc-900/30 rounded-b-xl">
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
