import { useState, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useParams, Link } from 'react-router-dom'
import { motion } from 'framer-motion'
import {
  ArrowLeft,
  FileCode,
  Hash,
  Loader2,
  GitPullRequest,
  ChevronRight,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import type { Repository, ReviewSummary } from '@/lib/api'
import { groupReviews } from '@/lib/review-utils'
import type { GroupedReview } from '@/lib/review-utils'
import { cn } from '@/lib/utils'

const stagger = { hidden: {}, show: { transition: { staggerChildren: 0.05 } } }
const fadeUp = { hidden: { opacity: 0, y: 10 }, show: { opacity: 1, y: 0, transition: { duration: 0.3 } } }

function StatCard({
  icon: Icon,
  label,
  value,
  sub,
  accent = 'blue',
}: {
  icon: React.ElementType
  label: string
  value: string
  sub?: string
  accent?: 'blue' | 'violet' | 'amber' | 'emerald' | 'rose'
}) {
  const accentColors = {
    blue: 'bg-blue-500/10 text-blue-600 dark:text-blue-400',
    violet: 'bg-violet-500/10 text-violet-600 dark:text-violet-400',
    amber: 'bg-amber-500/10 text-amber-600 dark:text-amber-400',
    emerald: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
    rose: 'bg-rose-500/10 text-rose-600 dark:text-rose-400',
  }

  return (
    <div className="bg-white dark:bg-[#15181e] rounded-[8px] border border-[#e1e3e6] dark:border-[#2d2f36] p-4 flex flex-col justify-between h-full">
      <div className={cn('h-8 w-8 rounded-[6px] flex items-center justify-center mb-3', accentColors[accent])}>
        <Icon className="h-4 w-4" />
      </div>

      <div>
        <p className="text-xl font-bold text-foreground font-mono tracking-tight">{value}</p>
        <p className="text-xs text-[#656a76] mt-0.5">{label}</p>
        {sub && <p className="text-[11px] text-[#8c919b] mt-0.5">{sub}</p>}
      </div>
    </div>
  )
}

function SeverityBadge({ severity, count }: { severity: 'critical' | 'high' | 'medium' | 'low'; count: number }) {
  if (count === 0) return null

  const styles = {
    critical: 'bg-rose-500/10 text-rose-600 border-rose-500/20 dark:text-rose-400',
    high: 'bg-orange-500/10 text-orange-600 border-orange-500/20 dark:text-orange-400',
    medium: 'bg-amber-500/10 text-amber-600 border-amber-500/20 dark:text-amber-400',
    low: 'bg-emerald-500/10 text-emerald-600 border-emerald-500/20 dark:text-emerald-400',
  }

  const labels = { critical: 'Critical', high: 'High', medium: 'Medium', low: 'Low' }

  return (
    <span className={cn('text-xs font-semibold px-2 py-0.5 rounded-[4px] border', styles[severity])}>
      {count} {labels[severity]}
    </span>
  )
}

function ReviewRow({ group, repoId }: { group: GroupedReview; repoId: string }) {
  const [isExpanded, setIsExpanded] = useState(false)
  const { original_review, latest_review, revisions, pr_number, pr_title } = group
  const hasRevisions = revisions.length > 0
  const displayCounts = latest_review.severity_counts

  return (
    <motion.div variants={fadeUp} className="border-b border-[#e1e3e6] dark:border-[#2d2f36] last:border-0">
      <div
        className="flex items-center gap-4 px-5 py-4 hover:bg-[#f1f2f3] dark:hover:bg-[#1e2025] transition-colors cursor-pointer group"
        onClick={() => hasRevisions && setIsExpanded(!isExpanded)}
      >
        <div className="h-9 w-9 rounded-[6px] bg-[#f1f2f3] dark:bg-[#1e2025] flex items-center justify-center shrink-0">
          <GitPullRequest className="h-4 w-4 text-[#8c919b]" />
        </div>

        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1">
            <p className="text-sm font-semibold text-foreground truncate">{pr_title}</p>
            {hasRevisions && (
              <span className="shrink-0 text-xs font-semibold px-2 py-0.5 rounded-[4px] bg-blue-500/10 text-blue-600 dark:text-blue-400 border border-blue-500/20">
                {revisions.length} re-review{revisions.length > 1 ? 's' : ''}
              </span>
            )}
          </div>

          <div className="flex items-center gap-2">
            <span className="text-xs font-mono text-[#8c919b]">#{pr_number}</span>
            {(displayCounts.critical > 0 || displayCounts.high > 0 || displayCounts.medium > 0 || displayCounts.low > 0) && (
              <span className="text-[#8c919b]">·</span>
            )}
            <SeverityBadge severity="critical" count={displayCounts.critical} />
            <SeverityBadge severity="high" count={displayCounts.high} />
            <SeverityBadge severity="medium" count={displayCounts.medium} />
            <SeverityBadge severity="low" count={displayCounts.low} />
          </div>
        </div>

        <div className="flex items-center gap-3 shrink-0">
          <span className="text-sm text-[#8c919b]">
            {new Date(original_review.reviewed_at).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}
          </span>

          <Link
            to={`/repos/${repoId}/reviews/${pr_number}?id=${original_review.id}`}
            onClick={(e) => e.stopPropagation()}
            className="p-1.5 rounded-[4px] hover:bg-[#2264d6]/10 text-[#8c919b] hover:text-[#2264d6] transition-colors"
          >
            <ChevronRight className="h-4 w-4" />
          </Link>
        </div>
      </div>

      {hasRevisions && isExpanded && (
        <motion.div
          initial={{ opacity: 0, height: 0 }}
          animate={{ opacity: 1, height: 'auto' }}
          className="ml-14 border-l border-blue-500/20 pl-4 space-y-1 mb-3"
        >
          {revisions.map((rev, idx) => (
            <Link
              key={rev.id}
              to={`/repos/${repoId}/reviews/${pr_number}?id=${rev.id}`}
              className="flex items-center justify-between py-2.5 px-3 hover:bg-[#f1f2f3] dark:hover:bg-[#1e2025] rounded-[6px] transition-colors"
            >
              <div className="flex items-center gap-2">
                <span className="text-xs font-bold px-2 py-0.5 rounded-[4px] bg-blue-500/10 text-blue-600 dark:text-blue-400 border border-blue-500/20">
                  V{rev.revision}
                </span>
                {idx === revisions.length - 1 && (
                  <span className="text-xs font-semibold px-2 py-0.5 rounded-[4px] bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border border-emerald-500/20">
                    Latest
                  </span>
                )}
              </div>

              <div className="flex items-center gap-3">
                <div className="flex items-center gap-1.5">
                  <SeverityBadge severity="critical" count={rev.severity_counts.critical} />
                  <SeverityBadge severity="high" count={rev.severity_counts.high} />
                  <SeverityBadge severity="medium" count={rev.severity_counts.medium} />
                  <SeverityBadge severity="low" count={rev.severity_counts.low} />
                </div>
                <span className="text-xs text-[#8c919b]">
                  {new Date(rev.reviewed_at).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}
                </span>
              </div>
            </Link>
          ))}
        </motion.div>
      )}
    </motion.div>
  )
}

export default function RepoDetailPage() {
  const { repoId } = useParams<{ repoId: string }>()
  const id = parseInt(repoId ?? '0', 10)

  const { data: repo, isLoading: repoLoading } = useQuery<Repository>({
    queryKey: ['repo', repoId],
    queryFn: () => api.repos.get(id),
    enabled: !!repoId,
  })

  const { data: reviews } = useQuery<ReviewSummary[]>({
    queryKey: ['reviews', id],
    queryFn: () => api.reviews.list(id),
    enabled: !!repoId,
  })

  const groupedReviews = useMemo(() => (reviews ? groupReviews(reviews) : []), [reviews])

  if (repoLoading) {
    return (
      <div className="flex flex-col items-center justify-center py-32 gap-3">
        <Loader2 className="h-6 w-6 animate-spin text-[#2264d6]" />
        <p className="text-sm text-[#656a76]">Loading repository...</p>
      </div>
    )
  }

  if (!repo) {
    return (
      <div className="flex flex-col items-center justify-center py-32 text-center gap-4">
        <p className="text-[#656a76]">Repository not found.</p>
        <Button asChild variant="secondary">
          <Link to="/">Back to Dashboard</Link>
        </Button>
      </div>
    )
  }

  const [org, repoName] = repo.full_name.split('/')

  return (
    <motion.div className="space-y-6" initial="hidden" animate="show" variants={stagger}>
      <motion.div variants={fadeUp} className="flex items-center gap-4">
        <Link to="/" className="p-2 rounded-[6px] hover:bg-[#f1f2f3] dark:hover:bg-[#1e2025] text-[#8c919b] transition-colors shrink-0">
          <ArrowLeft className="h-4 w-4" />
        </Link>

        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-3 flex-wrap">
            <h1 className="text-xl font-bold text-foreground">
              <span className="text-[#8c919b] font-normal">{org}/</span>
              {repoName}
            </h1>
          </div>
          <p className="text-xs text-[#8c919b] font-mono truncate mt-0.5">{repo.clone_path}</p>
        </div>
      </motion.div>

      <motion.div variants={stagger} className="grid grid-cols-2 md:grid-cols-3 gap-3">
        <StatCard icon={Hash} label="Last indexed SHA" value={repo.last_indexed_sha ? repo.last_indexed_sha.slice(0, 7) : '—'} accent="amber" />
        <StatCard icon={GitPullRequest} label="Total reviews" value={groupedReviews.length.toLocaleString()} accent="blue" />
        <StatCard icon={FileCode} label="Open PRs" value="—" accent="violet" />
      </motion.div>

      <motion.div variants={fadeUp} className="space-y-3">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold text-[#656a76] uppercase tracking-wider">Recent Reviews</h2>
          <Link to={`/repos/${repoId}/reviews`} className="text-sm text-[#2264d6] hover:underline dark:text-[#2b89ff]">View all →</Link>
        </div>

        <div className="bg-white dark:bg-[#15181e] rounded-[8px] border border-[#e1e3e6] dark:border-[#2d2f36] overflow-hidden">
          {groupedReviews.length > 0 ? (
            <motion.div variants={stagger}>
              {groupedReviews.slice(0, 5).map(g => <ReviewRow key={g.pr_number} group={g} repoId={repoId!} />)}
            </motion.div>
          ) : (
            <div className="py-8 text-center text-sm text-[#8c919b]">
              No reviews yet — comment <code className="font-mono text-xs bg-[#f1f2f3] px-1.5 py-0.5 rounded dark:bg-[#1e2025]">/review</code> on a GitHub PR to get started.
            </div>
          )}
        </div>
      </motion.div>
    </motion.div>
  )
}
