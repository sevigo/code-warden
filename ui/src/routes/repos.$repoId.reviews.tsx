import { Fragment, useState, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useParams, Link } from 'react-router-dom'
import { motion } from 'framer-motion'
import {
  ArrowLeft,
  GitPullRequest,
  ChevronRight,
  Loader2,
  AlertTriangle,
  Search,
  GitBranch,
} from 'lucide-react'
import { api } from '@/lib/api'
import type { Repository, ReviewSummary } from '@/lib/api'
import { groupReviews } from '@/lib/review-utils'
import type { GroupedReview } from '@/lib/review-utils'

/**
 * CircleCI-Inspired Reviews Page
 * 
 * Design patterns:
 * - Pipeline-like list view
 * - Filterable by severity
 * - Clear status indicators
 * - Expandable revisions
 * - Quick actions
 */

const stagger = { hidden: {}, show: { transition: { staggerChildren: 0.05 } } }
const fadeUp = { hidden: { opacity: 0, y: 8 }, show: { opacity: 1, y: 0, transition: { duration: 0.28 } } }

type FilterType = 'all' | 'critical' | 'high' | 'medium' | 'low'

function relativeTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  const m = Math.floor(diff / 60000)
  if (m < 1) return 'just now'
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

function SeverityNum({ n, color }: { n: number; color: string }) {
  if (n === 0) return <span className="text-[#8c919b]/50">—</span>
  return <span className={`font-semibold ${color}`}>{n}</span>
}

// ── Review Group Row Component ────────────────────────────────────────────

function ReviewGroupRows({ group, repoId }: { group: GroupedReview; repoId: string }) {
  const [isExpanded, setIsExpanded] = useState(false)
  const { original_review, latest_review, revisions, pr_number, pr_title } = group
  const hasRevisions = revisions.length > 0
  const displayCounts = latest_review.severity_counts

  const severityColors = {
    critical: 'text-rose-500',
    high: 'text-orange-500',
    medium: 'text-amber-500',
    low: 'text-emerald-500',
  }

  return (
    <Fragment>
      {/* Main Row */}
      <motion.tr
        variants={fadeUp}
        className="border-b border-[#e1e3e6] dark:border-[#2d2f36] last:border-0 hover:bg-[#f1f2f3]/50 dark:hover:bg-[#1e2025]/50 transition-colors group cursor-pointer"
        onClick={() => hasRevisions && setIsExpanded(!isExpanded)}
      >
        {/* PR Info - Title prominently displayed */}
        <td className="py-3 pl-4 lg:pl-5" colSpan={2}>
          <div className="flex items-center gap-3">
            <div className="h-7 w-7 rounded-[5px] bg-[#f1f2f3] dark:bg-[#1e2025] flex items-center justify-center shrink-0">
              <GitPullRequest className="h-3.5 w-3.5 text-[#8c919b]" />
            </div>
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-2">
                <span className="text-sm font-semibold text-foreground truncate block max-w-[380px]">
                  {pr_title}
                </span>
                {hasRevisions && (
                  <span className="text-[11px] font-semibold px-2 py-0.5 rounded-[5px] bg-blue-500/10 text-blue-600 dark:text-blue-400 whitespace-nowrap">
                    {revisions.length} re-review{revisions.length > 1 ? 's' : ''}
                  </span>
                )}
              </div>
              <div className="text-xs text-[#8c919b] mt-0.5">
                #{pr_number} · reviewed {relativeTime(original_review.reviewed_at)}
              </div>
            </div>
          </div>
        </td>

        {/* Severity Counts */}
        <td className="py-3 px-3 text-center text-sm">
          <SeverityNum n={displayCounts.critical} color={severityColors.critical} />
        </td>
        <td className="py-3 px-3 text-center text-sm">
          <SeverityNum n={displayCounts.high} color={severityColors.high} />
        </td>
        <td className="py-3 px-3 text-center text-sm">
          <SeverityNum n={displayCounts.medium} color={severityColors.medium} />
        </td>
        <td className="py-3 px-3 text-center text-sm">
          <SeverityNum n={displayCounts.low} color={severityColors.low} />
        </td>

        {/* Date */}
        <td className="py-3 px-3 text-right whitespace-nowrap">
          <span 
            className="text-xs text-[#8c919b]" 
            title={new Date(original_review.reviewed_at).toLocaleString()}
          >
            {relativeTime(original_review.reviewed_at)}
          </span>
        </td>

        {/* Actions */}
        <td className="py-3 pr-4 lg:pr-5 text-right">
          <Link
            to={`/repos/${repoId}/reviews/${pr_number}?id=${original_review.id}`}
            onClick={(e) => e.stopPropagation()}
            className="p-1.5 rounded-[4px] hover:bg-[#2264d6]/10 text-[#8c919b] hover:text-[#2264d6] transition-colors inline-flex"
          >
            <ChevronRight className="h-4 w-4" />
          </Link>
        </td>
      </motion.tr>

      {/* Expanded Revisions */}
      {hasRevisions && isExpanded && revisions.map((rev, idx) => (
        <motion.tr
          key={rev.id}
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          className="bg-[#f1f2f3]/30 dark:bg-[#1e2025]/30 border-b border-blue-500/10 last:border-0"
        >
          <td className="py-2 pl-4 lg:pl-5">
            <div className="flex items-center gap-2">
              <span className="text-[10px] font-black px-1.5 py-0.5 rounded-[4px] bg-blue-500/10 text-blue-600 dark:text-blue-400">
                V{rev.revision}
              </span>
            </div>
          </td>
          <td className="py-2 px-3">
            <Link
              to={`/repos/${repoId}/reviews/${pr_number}?id=${rev.id}`}
              className="text-xs text-[#8c919b] hover:text-[#2264d6] transition-colors"
              onClick={(e) => e.stopPropagation()}
            >
              Re-review
            </Link>
            {idx === revisions.length - 1 && (
              <span className="text-[9px] font-bold px-1.5 py-0.5 rounded-[4px] bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 ml-2">
                Latest
              </span>
            )}
          </td>
          <td className="py-2 px-3 text-center text-xs opacity-60">
            <SeverityNum n={rev.severity_counts.critical} color={severityColors.critical} />
          </td>
          <td className="py-2 px-3 text-center text-xs opacity-60">
            <SeverityNum n={rev.severity_counts.high} color={severityColors.high} />
          </td>
          <td className="py-2 px-3 text-center text-xs opacity-60">
            <SeverityNum n={rev.severity_counts.medium} color={severityColors.medium} />
          </td>
          <td className="py-2 px-3 text-center text-xs opacity-60">
            <SeverityNum n={rev.severity_counts.low} color={severityColors.low} />
          </td>
          <td className="py-2 px-3 text-right whitespace-nowrap opacity-60">
            <span className="text-[10px] text-[#8c919b]" title={new Date(rev.reviewed_at).toLocaleString()}>
              {relativeTime(rev.reviewed_at)}
            </span>
          </td>
          <td className="py-2 pr-4 lg:pr-5 text-right">
            <Link
              to={`/repos/${repoId}/reviews/${pr_number}?id=${rev.id}`}
              className="p-1.5 rounded-[4px] hover:bg-[#2264d6]/10 text-[#8c919b] hover:text-[#2264d6] transition-colors inline-flex"
            >
              <ChevronRight className="h-3.5 w-3.5" />
            </Link>
          </td>
        </motion.tr>
      ))}
    </Fragment>
  )
}

// ── Main Page Component ─────────────────────────────────────────────────

export default function ReviewsPage() {
  const { repoId } = useParams<{ repoId: string }>()
  const id = parseInt(repoId ?? '0', 10)
  const [filter, setFilter] = useState<FilterType>('all')
  const [search, setSearch] = useState('')

  const { data: repo } = useQuery<Repository>({
    queryKey: ['repo', repoId],
    queryFn: () => api.repos.get(id),
    enabled: !!repoId,
  })

  const { data: reviews, isLoading } = useQuery<ReviewSummary[]>({
    queryKey: ['reviews', id],
    queryFn: () => api.reviews.list(id),
    enabled: !!repoId,
  })

  const filtered = useMemo(() => {
    let result = reviews || []
    
    if (filter !== 'all') {
      result = result.filter(r => r.severity_counts[filter] > 0)
    }
    
    if (search.trim()) {
      result = result.filter(r => 
        r.pr_title.toLowerCase().includes(search.toLowerCase()) ||
        String(r.pr_number).includes(search)
      )
    }
    
    return result
  }, [reviews, filter, search])

  const grouped = useMemo(() => groupReviews(filtered), [filtered])
  const repoName = repo?.full_name.split('/')[1] ?? '…'

  const severityFilters: { key: FilterType; label: string; color: string }[] = [
    { key: 'all', label: 'All', color: '' },
    { key: 'critical', label: 'Critical', color: 'text-rose-500' },
    { key: 'high', label: 'High', color: 'text-orange-500' },
    { key: 'medium', label: 'Medium', color: 'text-amber-500' },
    { key: 'low', label: 'Low', color: 'text-emerald-500' },
  ]

  // Calculate stats
  const stats = useMemo(() => {
    if (!reviews) return null
    return {
      total: reviews.length,
      critical: reviews.reduce((sum, r) => sum + r.severity_counts.critical, 0),
      high: reviews.reduce((sum, r) => sum + r.severity_counts.high, 0),
      medium: reviews.reduce((sum, r) => sum + r.severity_counts.medium, 0),
      low: reviews.reduce((sum, r) => sum + r.severity_counts.low, 0),
    }
  }, [reviews])

  return (
    <motion.div className="space-y-6" initial="hidden" animate="show" variants={stagger}>
      {/* Header */}
      <motion.div variants={fadeUp} className="flex items-center gap-3">
        <Link 
          to={`/repos/${repoId}`} 
          className="p-2 rounded-[6px] hover:bg-[#f1f2f3] dark:hover:bg-[#1e2025] text-[#8c919b] transition-colors shrink-0"
        >
          <ArrowLeft className="h-4 w-4" />
        </Link>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 text-sm text-[#8c919b] mb-0.5">
            <GitBranch className="h-3.5 w-3.5" />
            <span>{repoName}</span>
          </div>
          <h1 className="text-xl font-bold text-foreground">Code Reviews</h1>
        </div>
      </motion.div>

      {/* Stats Summary */}
      {stats && (
        <motion.div variants={fadeUp} className="grid grid-cols-5 gap-3">
          <div className="bg-white dark:bg-[#15181e] rounded-[6px] border border-[#e1e3e6] dark:border-[#2d2f36] p-3 text-center">
            <p className="text-lg font-bold text-foreground">{stats.total}</p>
            <p className="text-[10px] text-[#8c919b] uppercase tracking-wider">Total</p>
          </div>
          <div className="bg-rose-500/5 rounded-[6px] border border-rose-500/20 p-3 text-center">
            <p className="text-lg font-bold text-rose-500">{stats.critical}</p>
            <p className="text-[10px] text-rose-500/70 uppercase tracking-wider">Critical</p>
          </div>
          <div className="bg-orange-500/5 rounded-[6px] border border-orange-500/20 p-3 text-center">
            <p className="text-lg font-bold text-orange-500">{stats.high}</p>
            <p className="text-[10px] text-orange-500/70 uppercase tracking-wider">High</p>
          </div>
          <div className="bg-amber-500/5 rounded-[6px] border border-amber-500/20 p-3 text-center">
            <p className="text-lg font-bold text-amber-500">{stats.medium}</p>
            <p className="text-[10px] text-amber-500/70 uppercase tracking-wider">Medium</p>
          </div>
          <div className="bg-emerald-500/5 rounded-[6px] border border-emerald-500/20 p-3 text-center">
            <p className="text-lg font-bold text-emerald-500">{stats.low}</p>
            <p className="text-[10px] text-emerald-500/70 uppercase tracking-wider">Low</p>
          </div>
        </motion.div>
      )}

      {/* Filter Bar */}
      <motion.div variants={fadeUp} className="flex flex-col sm:flex-row gap-3 sm:items-center justify-between">
        {/* Filter Tabs */}
        <div className="flex items-center gap-1 bg-white dark:bg-[#15181e] rounded-[6px] border border-[#e1e3e6] dark:border-[#2d2f36] p-1 w-fit overflow-x-auto">
          {severityFilters.map(f => (
            <button
              key={f.key}
              onClick={() => setFilter(f.key)}
              className={`px-3 py-1.5 rounded-[4px] text-xs font-medium transition-all whitespace-nowrap ${
                filter === f.key
                  ? 'bg-[#2264d6]/10 text-[#2264d6]'
                  : 'text-[#8c919b] hover:text-foreground hover:bg-[#f1f2f3] dark:hover:bg-[#2d2f36]'
              }`}
            >
              {f.label}
              {f.key !== 'all' && reviews && (
                <span className="ml-1.5 text-[#8c919b]/50">
                  {reviews.filter(r => r.severity_counts[f.key as Exclude<FilterType, 'all'>] > 0).length}
                </span>
              )}
            </button>
          ))}
        </div>

        {/* Search */}
        <div className="relative">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-[#8c919b]" />
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search reviews..."
            className="w-full sm:w-64 pl-8 pr-3 py-1.5 rounded-[6px] bg-white dark:bg-[#15181e] border border-[#e1e3e6] dark:border-[#2d2f36] text-sm focus:outline-none focus:ring-1 focus:ring-[#2264d6]/30 placeholder:text-[#8c919b]"
          />
        </div>
      </motion.div>

      {/* Table */}
      {isLoading ? (
        <div className="flex items-center justify-center py-20 gap-2 text-[#8c919b]">
          <Loader2 className="h-4 w-4 animate-spin" />
          <span className="text-sm">Loading reviews...</span>
        </div>
      ) : filtered.length === 0 ? (
        <motion.div 
          variants={fadeUp} 
          className="bg-white dark:bg-[#15181e] rounded-[8px] border border-[#e1e3e6] dark:border-[#2d2f36] p-12 text-center"
        >
          <div className="h-12 w-12 rounded-2xl bg-[#f1f2f3] dark:bg-[#1e2025] flex items-center justify-center mx-auto mb-4">
            <GitPullRequest className="h-6 w-6 text-[#8c919b]" />
          </div>
          <p className="text-sm font-medium text-foreground mb-1">
            {search ? 'No matching reviews' : 'No reviews yet'}
          </p>
          <p className="text-sm text-[#8c919b]">
            Comment{' '}
            <code className="font-mono text-xs bg-[#f1f2f3] px-1.5 py-0.5 rounded dark:bg-[#1e2025]">
              /review
            </code>
            {' '}on a GitHub PR to trigger the first review.
          </p>
        </motion.div>
      ) : (
        <motion.div 
          variants={fadeUp} 
          className="bg-white dark:bg-[#15181e] rounded-[8px] border border-[#e1e3e6] dark:border-[#2d2f36] overflow-hidden shadow-[0_1px_1px_rgba(97,104,117,0.05)]"
        >
          <table className="w-full text-left">
            <thead>
              <tr className="border-b border-[#e1e3e6] dark:border-[#2d2f36]">
                <th className="text-left text-xs font-semibold text-[#8c919b] uppercase tracking-wider py-3 pl-4 lg:pl-5" colSpan={2}>
                  Pull Request
                </th>
                <th className="text-center text-xs font-semibold text-rose-500 uppercase tracking-wider py-3 px-3">
                  Crit
                </th>
                <th className="text-center text-xs font-semibold text-orange-500 uppercase tracking-wider py-3 px-3">
                  High
                </th>
                <th className="text-center text-xs font-semibold text-amber-500 uppercase tracking-wider py-3 px-3">
                  Med
                </th>
                <th className="text-center text-xs font-semibold text-emerald-500 uppercase tracking-wider py-3 px-3">
                  Low
                </th>
                <th className="text-right text-xs font-semibold text-[#8c919b] uppercase tracking-wider py-3 px-3">
                  Date
                </th>
                <th className="py-3 pr-4 lg:pr-5 w-10" />
              </tr>
            </thead>
            <motion.tbody variants={stagger}>
              {grouped.map(group => (
                <ReviewGroupRows key={group.pr_number} group={group} repoId={repoId!} />
              ))}
            </motion.tbody>
          </table>
        </motion.div>
      )}

      {/* Summary */}
      {reviews && reviews.length > 0 && (
        <motion.div variants={fadeUp} className="flex items-center gap-6 text-xs text-[#8c919b]">
          <span>{reviews.length} review{reviews.length !== 1 ? 's' : ''} total</span>
          {stats && stats.critical > 0 && (
            <span className="flex items-center gap-1 text-rose-500">
              <AlertTriangle className="h-3 w-3" />
              {stats.critical} critical findings
            </span>
          )}
        </motion.div>
      )}
    </motion.div>
  )
}
