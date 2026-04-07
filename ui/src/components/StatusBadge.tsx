import { RefreshCw, CheckCircle2, XCircle, Clock } from 'lucide-react'
import { cn } from '@/lib/utils'

/**
 * HashiCorp-Inspired Status Badge
 * 
 * Uses the mds-* utility classes from the design system.
 * Features:
 * - 5px border-radius (HashiCorp standard)
 * - Color-coded status states with semantic colors
 * - Icon + label pattern
 * - Light/dark mode support via CSS variables
 */

interface StatusBadgeProps {
  status: string | null | undefined
  size?: 'sm' | 'default' | 'lg'
  showLabel?: boolean
}

export default function StatusBadge({ 
  status, 
  size = 'default',
  showLabel = true 
}: StatusBadgeProps) {
  const base = 'inline-flex items-center gap-1.5 font-medium transition-colors'
  
  const sizeClass = {
    sm: 'text-[11px] px-2 py-0.5 rounded-[4px]',
    default: 'text-xs px-2.5 py-1 rounded-[5px]',
    lg: 'text-sm px-3 py-1.5 rounded-[5px]',
  }[size]
  
  const iconSize = {
    sm: 'h-3 w-3',
    default: 'h-3.5 w-3.5',
    lg: 'h-4 w-4',
  }[size]

  // Not indexed / Unknown state
  if (!status) {
    return (
      <span className={cn(base, sizeClass, 'mds-badge-neutral')}>
        <Clock className={cn(iconSize, 'shrink-0')} />
        {showLabel && 'Not Indexed'}
      </span>
    )
  }

  switch (status) {
    case 'scanning':
    case 'in_progress':
    case 'pending':
      return (
        <span className={cn(base, sizeClass, 'mds-badge-info')}>
          <RefreshCw className={cn(iconSize, 'shrink-0 animate-spin')} />
          {showLabel && 'Indexing'}
        </span>
      )
      
    case 'completed':
    case 'ready':
      return (
        <span className={cn(base, sizeClass, 'mds-badge-success')}>
          <CheckCircle2 className={cn(iconSize, 'shrink-0')} />
          {showLabel && 'Ready'}
        </span>
      )
      
    case 'failed':
    case 'error':
      return (
        <span className={cn(base, sizeClass, 'mds-badge-error')}>
          <XCircle className={cn(iconSize, 'shrink-0')} />
          {showLabel && 'Failed'}
        </span>
      )
      
    default:
      return (
        <span className={cn(base, sizeClass, 'mds-badge-neutral')}>
          <Clock className={cn(iconSize, 'shrink-0')} />
          {showLabel && 'Not Indexed'}
        </span>
      )
  }
}

/**
 * Compact status dot for tight spaces (e.g., sidebar)
 */
export function StatusDot({ status }: { status: string | null | undefined }) {
  const base = 'h-2 w-2 rounded-full shrink-0'
  
  if (!status) {
    return <div className={cn(base, 'bg-[#656a76]')} title="Not indexed" />
  }
  
  switch (status) {
    case 'scanning':
    case 'in_progress':
    case 'pending':
      return (
        <div 
          className={cn(base, 'bg-[#3b82f6] animate-subtle-pulse')} 
          title="Indexing" 
        />
      )
    case 'completed':
    case 'ready':
      return (
        <div 
          className={cn(base, 'bg-[#10b981] shadow-[0_0_8px_rgba(16,185,129,0.4)]')} 
          title="Ready" 
        />
      )
    case 'failed':
    case 'error':
      return (
        <div 
          className={cn(base, 'bg-[#f43f5e] shadow-[0_0_8px_rgba(244,63,94,0.4)]')} 
          title="Failed" 
        />
      )
    default:
      return <div className={cn(base, 'bg-[#656a76]')} title="Not indexed" />
  }
}
