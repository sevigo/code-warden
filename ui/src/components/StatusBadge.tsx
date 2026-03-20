import { RefreshCw, CheckCircle2, XCircle, Clock } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import type { ScanState } from '@/lib/api'

interface StatusBadgeProps {
  status: ScanState['status'] | null | undefined
  size?: 'sm' | 'default'
}

export default function StatusBadge({ status, size = 'default' }: StatusBadgeProps) {
  const sizeClass = size === 'sm' ? 'text-[11px] px-2 py-0' : 'text-xs'
  const iconSize = size === 'sm' ? 'h-2.5 w-2.5' : 'h-3 w-3'

  if (!status) {
    return (
      <Badge variant="secondary" className={`gap-1.5 ${sizeClass}`}>
        <Clock className={iconSize} />
        Not Indexed
      </Badge>
    )
  }

  switch (status) {
    case 'scanning':
    case 'in_progress':
    case 'pending':
      return (
        <Badge className={`gap-1.5 ${sizeClass} bg-blue-500/20 text-blue-400 border-blue-500/30 hover:bg-blue-500/20`}>
          <RefreshCw className={`${iconSize} animate-spin`} />
          Indexing
        </Badge>
      )
    case 'completed':
      return (
        <Badge className={`gap-1.5 ${sizeClass} bg-emerald-500/20 text-emerald-400 border-emerald-500/30 hover:bg-emerald-500/20`}>
          <CheckCircle2 className={iconSize} />
          Ready
        </Badge>
      )
    case 'failed':
      return (
        <Badge variant="destructive" className={`gap-1.5 ${sizeClass}`}>
          <XCircle className={iconSize} />
          Failed
        </Badge>
      )
    default:
      return (
        <Badge variant="secondary" className={`gap-1.5 ${sizeClass}`}>
          <Clock className={iconSize} />
          Not Indexed
        </Badge>
      )
  }
}
