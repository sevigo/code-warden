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
} from 'lucide-react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
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
      if (data && (data.status === 'scanning' || data.status === 'pending')) {
        return 2000
      }
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
        <div className="h-8 bg-card rounded w-64" />
        <div className="h-40 bg-card rounded-lg border border-border" />
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
  const isScanning = scanState?.status === 'scanning' || scanState?.status === 'pending'
  const hasStats = stats && stats.chunks_count > 0

  const progressPercent =
    scanState?.progress && scanState.progress.files_total > 0
      ? Math.round((scanState.progress.files_done / scanState.progress.files_total) * 100)
      : 0

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center gap-4">
        <Link
          to="/"
          className="p-1.5 rounded-md hover:bg-muted text-muted-foreground transition-colors"
        >
          <ArrowLeft className="h-5 w-5" />
        </Link>
        <div className="flex items-center gap-3 flex-1 min-w-0">
          <div className="h-8 w-8 rounded-lg bg-muted flex items-center justify-center shrink-0">
            <GitBranch className="h-4 w-4 text-muted-foreground" />
          </div>
          <h1 className="text-xl font-semibold text-foreground truncate">{repo.full_name}</h1>
          <StatusBadge status={scanState?.status} />
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => triggerScan.mutate()}
          disabled={isScanning || triggerScan.isPending}
        >
          <RefreshCw className={`h-3.5 w-3.5 mr-1.5 ${isScanning ? 'animate-spin' : ''}`} />
          {isScanning ? 'Scanning...' : 'Re-scan'}
        </Button>
      </div>

      {/* Scan progress */}
      {isScanning && scanState && (
        <div className="rounded-xl border border-blue-500/20 bg-blue-500/5 p-4 space-y-2">
          <div className="flex items-center gap-2 text-sm text-blue-300">
            <RefreshCw className="h-4 w-4 animate-spin" />
            <span>{scanState.progress?.stage || 'Scanning in progress...'}</span>
          </div>
          <Progress value={progressPercent > 0 ? progressPercent : undefined} className="h-1.5" />
          {scanState.progress && scanState.progress.files_total > 0 && (
            <p className="text-xs text-blue-400/70">
              {scanState.progress.files_done} / {scanState.progress.files_total} files
            </p>
          )}
        </div>
      )}

      {/* Not indexed CTA */}
      {!scanState && (
        <div className="rounded-xl border border-dashed border-border bg-card p-8 text-center">
          <div className="h-12 w-12 rounded-full bg-primary/10 flex items-center justify-center mx-auto mb-4">
            <Layers className="h-6 w-6 text-primary" />
          </div>
          <h2 className="text-lg font-semibold mb-2">This repository hasn't been indexed yet</h2>
          <p className="text-muted-foreground text-sm mb-6">
            Run the initial scan to index the codebase and enable AI-powered exploration.
          </p>
          <Button onClick={() => triggerScan.mutate()} disabled={triggerScan.isPending}>
            <RefreshCw className="h-4 w-4 mr-2" />
            Run Initial Scan
          </Button>
        </div>
      )}

      {/* Two-column cards — show when indexed */}
      {isCompleted && (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {/* Explore with AI */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <MessageSquare className="h-4 w-4 text-primary" />
                Explore with AI
              </CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-4">
              <p className="text-sm text-muted-foreground">
                Ask questions about architecture, patterns, and functionality of this codebase.
              </p>
              <Button asChild className="w-full">
                <Link to={`/repos/${repoId}/chat`}>
                  Start Chat
                </Link>
              </Button>
            </CardContent>
          </Card>

          {/* Index Stats */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <Layers className="h-4 w-4 text-muted-foreground" />
                Index Stats
              </CardTitle>
            </CardHeader>
            <CardContent>
              {hasStats ? (
                <div className="space-y-3 text-sm">
                  <div className="flex justify-between">
                    <span className="text-muted-foreground">Chunks</span>
                    <span className="font-medium">{stats.chunks_count.toLocaleString()}</span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-muted-foreground">Files</span>
                    <span className="font-medium">{stats.files_count.toLocaleString()}</span>
                  </div>
                  {stats.last_indexed_sha && (
                    <div className="flex justify-between items-center">
                      <span className="text-muted-foreground">SHA</span>
                      <code className="font-mono text-xs">{stats.last_indexed_sha.slice(0, 7)}</code>
                    </div>
                  )}
                  {stats.last_scan_date && (
                    <div className="flex justify-between">
                      <span className="text-muted-foreground">Last scan</span>
                      <span>{new Date(stats.last_scan_date).toLocaleDateString()}</span>
                    </div>
                  )}
                </div>
              ) : (
                <div className="space-y-3 text-sm">
                  <p className="text-muted-foreground text-sm">
                    No index stats available yet. Run a scan to generate statistics.
                  </p>
                  <Button variant="outline" size="sm" onClick={() => triggerScan.mutate()} disabled={triggerScan.isPending}>
                    <RefreshCw className="h-3.5 w-3.5 mr-1.5" />
                    Scan Now
                  </Button>
                </div>
              )}
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  )
}
