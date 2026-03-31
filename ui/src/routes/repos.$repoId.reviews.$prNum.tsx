import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { useParams, Link } from 'react-router-dom'
import { motion } from 'framer-motion'
import {
  ArrowLeft,
  FileCode,
  ChevronDown,
  ChevronRight,
  CheckCircle2,
  Loader2,
  Lightbulb,
  Flag,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import type { ReviewDetail, ReviewFinding } from '@/lib/api'

const stagger = { hidden: {}, show: { transition: { staggerChildren: 0.04 } } }
const fadeUp  = { hidden: { opacity: 0, y: 8 }, show: { opacity: 1, y: 0, transition: { duration: 0.25 } } }

// ── Severity config ───────────────────────────────────────────────────────────

const SEV = {
  critical: {
    label: 'Critical',
    border: 'border-red-500/50',
    bg: 'bg-red-500/5',
    badge: 'bg-red-500/15 text-red-400',
    header: 'text-red-400',
    dot: 'bg-red-400',
    icon: '🔴',
  },
  warning: {
    label: 'Warning',
    border: 'border-orange-500/50',
    bg: 'bg-orange-500/5',
    badge: 'bg-orange-500/15 text-orange-400',
    header: 'text-orange-400',
    dot: 'bg-orange-400',
    icon: '🟠',
  },
  suggestion: {
    label: 'Suggestion',
    border: 'border-yellow-500/40',
    bg: 'bg-yellow-500/5',
    badge: 'bg-yellow-500/15 text-yellow-500',
    header: 'text-yellow-500',
    dot: 'bg-yellow-400',
    icon: '🟡',
  },
} as const

// ── Finding Card ──────────────────────────────────────────────────────────────

function FindingCard({
  finding,
  repoId,
  prNum,
}: {
  finding: ReviewFinding
  repoId: string
  prNum: string
}) {
  const [expanded, setExpanded] = useState(true)
  const [markedFP, setMarkedFP] = useState(false)
  const s = SEV[finding.severity]
  const id = parseInt(repoId, 10)
  const pr = parseInt(prNum, 10)

  const feedback = useMutation({
    mutationFn: () =>
      api.reviews.feedback(id, pr, { finding_id: finding.id, verdict: 'false_positive' }),
    onSuccess: () => setMarkedFP(true),
  })

  const hasLocation = finding.line_start > 0

  return (
    <motion.div
      variants={fadeUp}
      className={`rounded-xl border-l-4 ${s.border} ${s.bg} overflow-hidden`}
    >
      {/* Header row */}
      <button
        className="w-full flex items-start gap-3 p-4 text-left hover:bg-white/2 transition-colors"
        onClick={() => setExpanded(v => !v)}
      >
        <div className={`h-2 w-2 rounded-full mt-1.5 shrink-0 ${s.dot}`} />
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1 flex-wrap">
            <span className="text-sm font-semibold text-foreground">{finding.title}</span>
            <span className={`text-xs font-bold px-2 py-0.5 rounded-md uppercase tracking-wide ${s.badge}`}>
              {finding.category}
            </span>
          </div>
          {hasLocation && (
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground font-mono">
              <FileCode className="h-3 w-3 shrink-0" />
              <span className="truncate">{finding.file}</span>
              <span className="shrink-0 text-muted-foreground/50">
                :{finding.line_start}{finding.line_end && finding.line_end !== finding.line_start ? `–${finding.line_end}` : ''}
              </span>
            </div>
          )}
        </div>
        {expanded ? (
          <ChevronDown className="h-4 w-4 text-muted-foreground shrink-0 mt-0.5" />
        ) : (
          <ChevronRight className="h-4 w-4 text-muted-foreground shrink-0 mt-0.5" />
        )}
      </button>

      {/* Body */}
      {expanded && (
        <div className="px-4 pb-4 space-y-3 border-t border-border/20 pt-3">
          <p className="text-sm text-muted-foreground leading-relaxed">{finding.description}</p>

          {finding.suggestion && (
            <div className="flex gap-2.5 rounded-lg bg-primary/5 border border-primary/10 px-3 py-2.5">
              <Lightbulb className="h-4 w-4 text-primary shrink-0 mt-0.5" />
              <p className="text-sm text-foreground/90">{finding.suggestion}</p>
            </div>
          )}

          <div className="flex justify-end">
            {markedFP ? (
              <span className="flex items-center gap-1.5 text-xs text-emerald-400">
                <CheckCircle2 className="h-3.5 w-3.5" />
                Marked as false positive
              </span>
            ) : (
              <Button
                variant="ghost"
                size="sm"
                className="text-xs text-muted-foreground hover:text-foreground h-7 gap-1.5"
                onClick={() => feedback.mutate()}
                disabled={feedback.isPending}
              >
                {feedback.isPending ? (
                  <Loader2 className="h-3 w-3 animate-spin" />
                ) : (
                  <Flag className="h-3 w-3" />
                )}
                Mark as false positive
              </Button>
            )}
          </div>
        </div>
      )}
    </motion.div>
  )
}

// ── Group Section ─────────────────────────────────────────────────────────────

function FindingGroup({
  severity,
  findings,
  repoId,
  prNum,
}: {
  severity: 'critical' | 'warning' | 'suggestion'
  findings: ReviewFinding[]
  repoId: string
  prNum: string
}) {
  const s = SEV[severity]
  if (findings.length === 0) return null

  return (
    <motion.div variants={fadeUp} className="space-y-2.5">
      <div className={`flex items-center gap-2 text-sm font-semibold ${s.header}`}>
        <span>{s.icon}</span>
        <span>{s.label}</span>
        <span className={`ml-2 text-xs font-bold px-2 py-0.5 rounded-md ${s.badge}`}>
          {findings.length}
        </span>
      </div>
      {findings.map(f => (
        <FindingCard key={f.id} finding={f} repoId={repoId} prNum={prNum} />
      ))}
    </motion.div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

export default function ReviewDetailPage() {
  const { repoId, prNum } = useParams<{ repoId: string; prNum: string }>()
  const id = parseInt(repoId ?? '0', 10)
  const prNumber = parseInt(prNum ?? '0', 10)

  const { data: review, isLoading } = useQuery<ReviewDetail>({
    queryKey: ['review', id, prNumber],
    queryFn: () => api.reviews.get(id, prNumber),
    enabled: !!repoId && !!prNum,
  })

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-32 gap-2 text-muted-foreground">
        <Loader2 className="h-5 w-5 animate-spin" />
        <span className="text-sm">Loading review...</span>
      </div>
    )
  }

  if (!review) {
    return (
      <div className="flex flex-col items-center justify-center py-32 gap-4">
        <p className="text-muted-foreground">Review not found.</p>
        <Button asChild variant="outline">
          <Link to={`/repos/${repoId}/reviews`}>← Back to Reviews</Link>
        </Button>
      </div>
    )
  }

  const criticals   = review.findings.filter(f => f.severity === 'critical')
  const warnings    = review.findings.filter(f => f.severity === 'warning')
  const suggestions = review.findings.filter(f => f.severity === 'suggestion')

  return (
    <motion.div className="space-y-6" initial="hidden" animate="show" variants={stagger}>
      {/* Breadcrumb + header */}
      <motion.div variants={fadeUp} className="space-y-2">
        <Link
          to={`/repos/${repoId}/reviews`}
          className="inline-flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors"
        >
          <ArrowLeft className="h-3.5 w-3.5" />
          Back to Reviews
        </Link>

        <div className="flex items-start gap-3 flex-wrap">
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 mb-1">
              <span className="font-mono text-sm text-muted-foreground">#{review.pr_number}</span>
              <span className="h-1 w-1 rounded-full bg-border" />
              <span className="text-xs text-muted-foreground">
                {new Date(review.reviewed_at).toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' })}
              </span>
            </div>
            <h1 className="text-2xl font-bold text-foreground tracking-tight">{review.pr_title}</h1>
          </div>
        </div>

        {/* Severity summary */}
        <div className="flex items-center gap-2 flex-wrap">
          <span className={`flex items-center gap-1 px-2.5 py-1 rounded-lg text-xs font-semibold ${SEV.critical.badge}`}>
            {review.severity_counts.critical} critical
          </span>
          <span className={`flex items-center gap-1 px-2.5 py-1 rounded-lg text-xs font-semibold ${SEV.warning.badge}`}>
            {review.severity_counts.warning} warnings
          </span>
          <span className={`flex items-center gap-1 px-2.5 py-1 rounded-lg text-xs font-semibold ${SEV.suggestion.badge}`}>
            {review.severity_counts.suggestion} suggestions
          </span>
          <span className="text-xs text-muted-foreground ml-1">
            {review.findings.length} total finding{review.findings.length !== 1 ? 's' : ''}
          </span>
        </div>
      </motion.div>

      {/* Findings grouped by severity */}
      <motion.div variants={stagger} className="space-y-6">
        <FindingGroup severity="critical"   findings={criticals}   repoId={repoId!} prNum={prNum!} />
        <FindingGroup severity="warning"    findings={warnings}    repoId={repoId!} prNum={prNum!} />
        <FindingGroup severity="suggestion" findings={suggestions} repoId={repoId!} prNum={prNum!} />
      </motion.div>
    </motion.div>
  )
}
