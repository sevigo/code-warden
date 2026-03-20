import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { GitBranch, RefreshCw, CheckCircle2, XCircle, Clock } from 'lucide-react'
import { Card, CardContent } from '@/components/ui/card'
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
      <Badge variant="secondary" className="gap-1.5">
        <Clock className="h-3 w-3" />
        Not Indexed
      </Badge>
    )
  }

  switch (status) {
    case 'scanning':
    case 'pending':
      return (
        <Badge className="gap-1.5 bg-blue-500/20 text-blue-400 border-blue-500/30 hover:bg-blue-500/20">
          <RefreshCw className="h-3 w-3 animate-spin" />
          Indexing
        </Badge>
      )
    case 'completed':
      return (
        <Badge className="gap-1.5 bg-green-500/20 text-green-400 border-green-500/30 hover:bg-green-500/20">
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

  const cardContent = (
    <Card className="hover:border-primary/40 transition-colors">
      <CardContent className="p-4">
        <div className="flex items-center gap-4">
          {/* Icon */}
          <div className="h-10 w-10 rounded-lg bg-muted flex items-center justify-center shrink-0">
            <GitBranch className="h-5 w-5 text-muted-foreground" />
          </div>

          {/* Info */}
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 mb-0.5">
              <h3 className="font-medium text-foreground truncate">{repo.full_name}</h3>
              <StatusBadge status={scanState?.status} />
            </div>
            <p className="text-xs text-muted-foreground truncate">{repo.clone_path}</p>

            {shortSHA && (
              <p className="text-xs text-muted-foreground mt-0.5">
                SHA: <code className="font-mono">{shortSHA}</code>
              </p>
            )}
          </div>

          {/* Actions */}
          <div className="flex items-center gap-2 shrink-0" onClick={(e) => e.preventDefault()}>
            {(noScan || isFailed) && (
              <Button
                size="sm"
                onClick={(e) => {
                  e.preventDefault()
                  e.stopPropagation()
                  onScan?.()
                }}
              >
                Scan
              </Button>
            )}

            {isScanning && (
              <Button size="sm" disabled variant="secondary">
                Scanning...
              </Button>
            )}

            {isCompleted && (
              <Button size="sm" asChild>
                <Link to={`/repos/${repo.id}`} onClick={(e) => e.stopPropagation()}>
                  Explore →
                </Link>
              </Button>
            )}
          </div>
        </div>

        {/* Scan progress bar */}
        {isScanning && (
          <div className="mt-3">
            <div className="flex items-center gap-2 text-xs text-muted-foreground mb-1.5">
              <RefreshCw className="h-3 w-3 animate-spin" />
              <span>{scanState?.progress?.stage || 'scanning'}</span>
              {scanState?.progress?.current_file && (
                <span className="truncate text-zinc-500">— {scanState.progress.current_file}</span>
              )}
            </div>
            <Progress value={progressPercent > 0 ? progressPercent : undefined} className="h-1.5" />
            {scanState?.progress && scanState.progress.files_total > 0 && (
              <p className="text-xs text-muted-foreground mt-1">
                {scanState.progress.files_done} / {scanState.progress.files_total} files
              </p>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  )

  if (isCompleted) {
    return (
      <Link to={`/repos/${repo.id}`} className="block">
        {cardContent}
      </Link>
    )
  }

  return cardContent
}
