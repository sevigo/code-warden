import { RefreshCw, CheckCircle2, XCircle, Clock } from 'lucide-react'

interface StatusBadgeProps {
  status: string | null | undefined
  size?: 'sm' | 'default'
}

export default function StatusBadge({ status, size = 'default' }: StatusBadgeProps) {
  const base = 'inline-flex items-center gap-1.5 rounded-full font-medium'
  const sizeClass = size === 'sm'
    ? 'text-[11px] px-2 py-0.5'
    : 'text-xs px-2.5 py-1'
  const iconSize = size === 'sm' ? 'h-2.5 w-2.5' : 'h-3 w-3'

  if (!status) {
    return (
      <span className={`${base} ${sizeClass} bg-zinc-500/10 text-zinc-500`}>
        <Clock className={iconSize} />
        Not Indexed
      </span>
    )
  }

  switch (status) {
    case 'scanning':
    case 'in_progress':
    case 'pending':
      return (
        <span className={`${base} ${sizeClass} bg-blue-500/10 text-blue-400`}>
          <RefreshCw className={`${iconSize} animate-spin`} />
          Indexing
        </span>
      )
    case 'completed':
      return (
        <span className={`${base} ${sizeClass} bg-emerald-500/10 text-emerald-400`}>
          <CheckCircle2 className={iconSize} />
          Ready
        </span>
      )
    case 'failed':
      return (
        <span className={`${base} ${sizeClass} bg-red-500/10 text-red-400`}>
          <XCircle className={iconSize} />
          Failed
        </span>
      )
    default:
      return (
        <span className={`${base} ${sizeClass} bg-zinc-500/10 text-zinc-500`}>
          <Clock className={iconSize} />
          Not Indexed
        </span>
      )
  }
}
