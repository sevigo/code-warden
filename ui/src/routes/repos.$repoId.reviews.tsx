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
} from 'lucide-react'
import { api } from '@/lib/api'
import type { Repository, ReviewSummary } from '@/lib/api'
import { groupReviews } from '@/lib/review-utils'
import type { GroupedReview } from '@/lib/review-utils'

const stagger = { hidden: {}, show: { transition: { staggerChildren: 0.05 } } }
const fadeUp  = { hidden: { opacity: 0, y: 8 }, show: { opacity: 1, y: 0, transition: { duration: 0.28 } } }

type Filter = 'all' | 'critical' | 'high' | 'medium' | 'low'

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
  if (n === 0) return <span className="text-muted-foreground/30">—</span>
  return <span className={`font-semibold ${color}`}>{n}</span>
}

export default function ReviewsPage() {
  const { repoId } = useParams<{ repoId: string }>()
  const id = parseInt(repoId ?? '0', 10)
  const [filter, setFilter] = useState<Filter>('all')

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

  const filtered = reviews?.filter(r => {
    if (filter === 'all') return true
    return r.severity_counts[filter] > 0
  }) ?? []

  const grouped = useMemo(() => groupReviews(filtered), [filtered])

  const repoName = repo?.full_name.split('/')[1] ?? '…'

  const FILTERS: { key: Filter; label: string }[] = [
    { key: 'all',      label: 'All' },
    { key: 'critical', label: 'Critical' },
    { key: 'high',     label: 'High' },
    { key: 'medium',   label: 'Medium' },
    { key: 'low',      label: 'Low' },
  ]

  return (
    <motion.div className="space-y-6" initial="hidden" animate="show" variants={stagger}>
      {/* Header */}
      <motion.div variants={fadeUp} className="flex items-center gap-3">
        <Link to={`/repos/${repoId}`} className="p-2 rounded-lg hover:bg-accent/50 text-muted-foreground transition-colors shrink-0">
          <ArrowLeft className="h-4 w-4" />
        </Link>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 text-sm text-muted-foreground mb-0.5">
            <span>{repoName}</span>
          </div>
          <h1 className="text-2xl font-bold text-foreground tracking-tight">Code Reviews</h1>
        </div>
      </motion.div>

      {/* Filter tabs */}
      <motion.div variants={fadeUp} className="flex items-center gap-1 bg-card rounded-xl p-1 w-fit">
        {FILTERS.map(f => (
          <button
            key={f.key}
            onClick={() => setFilter(f.key)}
            className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-all ${
              filter === f.key
                ? 'bg-primary/10 text-primary'
                : 'text-muted-foreground hover:text-foreground hover:bg-accent/50'
            }`}
          >
            {f.label}
            {f.key !== 'all' && reviews && (
              <span className="ml-1.5 text-muted-foreground/50">
                {reviews.filter(r => r.severity_counts[f.key as Exclude<Filter, 'all'>] > 0).length}
              </span>
            )}
          </button>
        ))}
      </motion.div>

      {/* Table */}
      {isLoading ? (
        <div className="flex items-center justify-center py-20 gap-2 text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          <span className="text-sm">Loading reviews...</span>
        </div>
      ) : filtered.length === 0 ? (
        <motion.div variants={fadeUp} className="rounded-2xl bg-card p-12 text-center">
          <div className="h-12 w-12 rounded-2xl bg-muted/30 flex items-center justify-center mx-auto mb-4">
            <GitPullRequest className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-sm font-medium text-foreground mb-1">No reviews yet</p>
          <p className="text-sm text-muted-foreground">
            Comment <code className="font-mono text-xs bg-accent/50 px-1.5 py-0.5 rounded">/review</code> on a GitHub PR to trigger the first review.
          </p>
        </motion.div>
      ) : (
        <motion.div variants={fadeUp} className="rounded-2xl bg-card overflow-hidden border border-border shadow-sm dark:border-transparent dark:shadow-none">
          <table className="w-full text-left">
            <thead>
              <tr className="border-b border-border dark:border-border/20 text-[10px]">
                <th className="font-semibold text-muted-foreground uppercase tracking-wider py-4 pl-4 lg:pl-6">PR</th>
                <th className="font-semibold text-muted-foreground uppercase tracking-wider py-4 px-4">Title</th>
                <th className="text-center font-semibold text-rose-400 uppercase tracking-wider py-4 px-3">Crit</th>
                <th className="text-center font-semibold text-orange-400 uppercase tracking-wider py-4 px-3">High</th>
                <th className="text-center font-semibold text-amber-400 uppercase tracking-wider py-4 px-3">Med</th>
                <th className="text-center font-semibold text-emerald-400 uppercase tracking-wider py-4 px-3">Low</th>
                <th className="text-right font-semibold text-muted-foreground uppercase tracking-wider py-4 pr-4 lg:pr-6">Date</th>
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

      {/* Summary bar */}
      {reviews && reviews.length > 0 && (
        <motion.div variants={fadeUp} className="flex items-center gap-6 px-1 text-xs text-muted-foreground">
          <span>{reviews.length} review{reviews.length !== 1 ? 's' : ''} total</span>
          <span className="flex items-center gap-1 text-rose-400">
            <AlertTriangle className="h-3 w-3" />
            {reviews.reduce((a, r) => a + r.severity_counts.critical, 0)} critical findings
          </span>
        </motion.div>
      )}
    </motion.div>
  )
}

function ReviewGroupRows({ group, repoId }: { group: GroupedReview; repoId: string }) {
  const [isExpanded, setIsExpanded] = useState(false)
  const { original_review, latest_review, revisions, pr_number, pr_title } = group
  const hasRevisions = revisions.length > 0
  // Show latest severity state so users see the current standing of the PR
  const displayCounts = latest_review.severity_counts

  return (
    <Fragment>
      <motion.tr
        variants={fadeUp}
        className="border-b border-border/20 last:border-0 hover:bg-accent/20 transition-colors group cursor-pointer"
        onClick={() => hasRevisions && setIsExpanded(!isExpanded)}
      >
        <td className="py-3 pl-4">
          <div className="flex items-center gap-2">
            <div className="h-6 w-6 rounded-md bg-accent/50 flex items-center justify-center shrink-0">
              <GitPullRequest className="h-3 w-3 text-muted-foreground" />
            </div>
            <span className="font-mono text-xs text-muted-foreground">#{pr_number}</span>
          </div>
        </td>
        <td className="py-4 px-4">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium text-foreground truncate block max-w-[300px]">
              {pr_title}
            </span>
            {hasRevisions && (
              <span className="text-[9px] font-bold px-1.5 py-0.5 rounded bg-blue-500/10 text-blue-400 border border-blue-500/20 uppercase tracking-tighter">
                {revisions.length} re-review{revisions.length > 1 ? 's' : ''}
              </span>
            )}
          </div>
        </td>
        <td className="py-3 px-3 text-center text-sm">
          <SeverityNum n={displayCounts.critical} color="text-rose-400" />
        </td>
        <td className="py-3 px-3 text-center text-sm">
          <SeverityNum n={displayCounts.high} color="text-orange-400" />
        </td>
        <td className="py-3 px-3 text-center text-sm">
          <SeverityNum n={displayCounts.medium} color="text-amber-400" />
        </td>
        <td className="py-3 px-3 text-center text-sm">
          <SeverityNum n={displayCounts.low} color="text-emerald-400" />
        </td>
        <td className="py-3 px-4 text-right whitespace-nowrap">
          <span className="text-xs text-muted-foreground" title={new Date(original_review.reviewed_at).toLocaleString()}>
            {relativeTime(original_review.reviewed_at)}
          </span>
        </td>
        <td className="py-3 pr-4 text-right">
          <div className="flex items-center justify-end gap-2">
            <Link
              to={`/repos/${repoId}/reviews/${pr_number}?id=${original_review.id}`}
              onClick={(e) => e.stopPropagation()}
              className="p-1 hover:bg-accent rounded-md transition-colors"
            >
              <ChevronRight className="h-4 w-4 text-muted-foreground/30 group-hover:text-muted-foreground" />
            </Link>
          </div>
        </td>
      </motion.tr>

      {hasRevisions && isExpanded && revisions.map((rev, idx) => (
        <motion.tr
          key={rev.id}
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          className="bg-accent/5 border-b border-blue-500/10 last:border-0 hover:bg-accent/10 transition-colors group/rev cursor-pointer"
        >
          <td className="py-2 pl-10">
            <div className="flex items-center gap-2">
              <span className="text-[10px] font-black px-1 py-0.5 rounded bg-blue-500/10 text-blue-400 border border-blue-500/20 uppercase tracking-tighter">
                V{rev.revision}
              </span>
            </div>
          </td>
          <td className="py-2 px-4">
            <div className="flex items-center gap-2">
              <Link
                to={`/repos/${repoId}/reviews/${pr_number}?id=${rev.id}`}
                className="text-xs text-muted-foreground hover:text-primary transition-colors"
              >
                Re-review
              </Link>
              {idx === revisions.length - 1 && (
                <span className="text-[9px] font-bold px-1 py-0.5 rounded bg-emerald-500/10 text-emerald-400 border border-emerald-500/20 uppercase tracking-tighter">
                  Latest
                </span>
              )}
            </div>
          </td>
          <td className="py-2 px-3 text-center text-xs opacity-60">
            <SeverityNum n={rev.severity_counts.critical} color="text-rose-400" />
          </td>
          <td className="py-2 px-3 text-center text-xs opacity-60">
            <SeverityNum n={rev.severity_counts.high} color="text-orange-400" />
          </td>
          <td className="py-2 px-3 text-center text-xs opacity-60">
            <SeverityNum n={rev.severity_counts.medium} color="text-amber-400" />
          </td>
          <td className="py-2 px-3 text-center text-xs opacity-60">
            <SeverityNum n={rev.severity_counts.low} color="text-emerald-400" />
          </td>
          <td className="py-2 px-4 text-right whitespace-nowrap opacity-60">
            <span className="text-[10px] text-muted-foreground" title={new Date(rev.reviewed_at).toLocaleString()}>
              {relativeTime(rev.reviewed_at)}
            </span>
          </td>
          <td className="py-2 pr-4 text-right">
            <Link
              to={`/repos/${repoId}/reviews/${pr_number}?id=${rev.id}`}
              className="inline-block p-1 hover:bg-accent rounded-md transition-colors"
            >
              <ChevronRight className="h-3 w-3 text-muted-foreground/20 group-hover/rev:text-muted-foreground" />
            </Link>
          </td>
        </motion.tr>
      ))}
    </Fragment>
  )
}
