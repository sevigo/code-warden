import { RefreshCw, CheckCircle2, XCircle, Clock } from 'lucide-react'

interface StatusBadgeProps {
  status: string | null | undefined
  size?: 'sm' | 'default'
}

export default function StatusBadge({ status, size = 'default' }: StatusBadgeProps) {
  const base = 'inline-flex items-center gap-1.5 rounded-full font-medium'
  const sizeClass = size === 'sm'
    ? 'text-xs px-2.5 py-0.5'
    : 'text-sm px-3 py-1'
  const iconSize = size === 'sm' ? 'h-3 w-3' : 'h-3.5 w-3.5'

  if (!status) {
    return (
      <span className={`${base} ${sizeClass} bg-zinc-50 border border-zinc-200 text-zinc-700 dark:bg-zinc-500/10 dark:border-zinc-500/20 dark:text-zinc-500`}>
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
        <span className={`${base} ${sizeClass} bg-blue-50 border border-blue-200 text-blue-700 dark:bg-blue-500/10 dark:border-blue-500/20 dark:text-blue-400`}>
          <RefreshCw className={`${iconSize} animate-spin`} />
          Indexing
        </span>
      )
    case 'completed':
      return (
        <span className={`${base} ${sizeClass} bg-emerald-50 border border-emerald-200 text-emerald-700 dark:bg-emerald-500/10 dark:border-emerald-500/20 dark:text-emerald-400`}>
          <CheckCircle2 className={iconSize} />
          Ready
        </span>
      )
    case 'failed':
      return (
        <span className={`${base} ${sizeClass} bg-red-50 border border-red-200 text-red-700 dark:bg-red-500/10 dark:border-red-500/20 dark:text-red-400`}>
          <XCircle className={iconSize} />
          Failed
        </span>
      )
    default:
      return (
        <span className={`${base} ${sizeClass} bg-zinc-50 border border-zinc-200 text-zinc-700 dark:bg-zinc-500/10 dark:border-zinc-500/20 dark:text-zinc-500`}>
          <Clock className={iconSize} />
          Not Indexed
        </span>
      )
  }
}
