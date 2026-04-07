import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { useLocation, useParams, Link } from 'react-router-dom'
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
  Brain,
  Wrench,
  Copy,
  Check,
  Eye,
  RefreshCw,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { api } from '@/lib/api'
import type { ReviewDetail, ReviewFinding } from '@/lib/api'

const stagger = { hidden: {}, show: { transition: { staggerChildren: 0.04 } } }
const fadeUp  = { hidden: { opacity: 0, y: 8 }, show: { opacity: 1, y: 0, transition: { duration: 0.25 } } }

// ── Severity config ───────────────────────────────────────────────────────────

const SEV = {
  critical: {
    label: 'Critical',
    border: 'border-rose-500/50',
    bg: 'bg-rose-500/5',
    badge: 'bg-rose-500/15 text-rose-400',
    header: 'text-rose-400',
    dot: 'bg-rose-400',
    icon: '🔴',
  },
  high: {
    label: 'High',
    border: 'border-orange-500/50',
    bg: 'bg-orange-500/5',
    badge: 'bg-orange-500/15 text-orange-500',
    header: 'text-orange-500',
    dot: 'bg-orange-500',
    icon: '🟠',
  },
  medium: {
    label: 'Medium',
    border: 'border-amber-500/40',
    bg: 'bg-amber-500/5',
    badge: 'bg-amber-500/15 text-amber-500',
    header: 'text-amber-500',
    dot: 'bg-amber-500',
    icon: '🟡',
  },
  low: {
    label: 'Low',
    border: 'border-emerald-500/40',
    bg: 'bg-emerald-500/5',
    badge: 'bg-emerald-500/15 text-emerald-400',
    header: 'text-emerald-400',
    dot: 'bg-emerald-400',
    icon: '🟢',
  },
} as const

// ── Helpers ──────────────────────────────────────────────────────────────────

function ParsedDescription({ text }: { text: string }) {
  // Regex to split by **Observation:**, **Rationale:**, or **Fix:**
  const parts = text.split(/(\*\*(?:Observation|Rationale|Fix):\*\*)/g);
  
  if (parts.length <= 1) {
    return <p className="text-sm text-muted-foreground leading-relaxed">{text}</p>;
  }

  const sections: { label: string; content: string }[] = [];
  for (let i = 1; i < parts.length; i += 2) {
    const label = parts[i].replace(/\*\*/g, '').replace(':', '');
    const content = parts[i + 1]?.trim();
    if (content) sections.push({ label, content });
  }

  return (
    <div className="space-y-4">
      {sections.map((s, i) => {
        const Icon = s.label === 'Observation' ? Eye : s.label === 'Rationale' ? Brain : Wrench;
        const color = s.label === 'Observation' ? 'text-blue-400 bg-blue-400/10' : s.label === 'Rationale' ? 'text-purple-400 bg-purple-400/10' : 'text-emerald-400 bg-emerald-400/10';
        
        return (
          <div key={i} className="group">
            <div className="flex items-center gap-2 mb-1.5">
              <div className={`p-1 rounded-md ${color}`}>
                <Icon className="h-3 w-3" />
              </div>
              <span className="text-xs font-bold uppercase tracking-wider text-foreground/70">{s.label}</span>
            </div>
            <p className="text-sm text-muted-foreground/90 leading-relaxed pl-7 border-l border-border/10">
              {s.content}
            </p>
          </div>
        )
      })}
    </div>
  );
}

function CodeSuggestion({ code }: { code: string }) {
  const [copied, setCopied] = useState(false);

  const copy = () => {
    navigator.clipboard.writeText(code);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <div className="relative group rounded-xl bg-[#0d1117] border border-white/10 overflow-hidden shadow-2xl">
      <div className="flex items-center justify-between px-4 py-2 bg-white/5 border-b border-white/10">
        <div className="flex gap-1.5">
          <div className="h-2.5 w-2.5 rounded-full bg-red-400/40" />
          <div className="h-2.5 w-2.5 rounded-full bg-amber-400/40" />
          <div className="h-2.5 w-2.5 rounded-full bg-emerald-400/40" />
        </div>
        <button 
          onClick={copy}
          className="p-1.5 rounded-md hover:bg-white/10 text-muted-foreground hover:text-foreground transition-all"
        >
          {copied ? <Check className="h-3.5 w-3.5 text-emerald-400" /> : <Copy className="h-3.5 w-3.5" />}
        </button>
      </div>
      <pre className="p-4 text-[13px] font-mono leading-relaxed overflow-x-auto text-blue-100/90 selection:bg-blue-500/30">
        <code>{code}</code>
      </pre>
    </div>
  );
}

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
          <div className="flex items-center gap-2 mb-1.5 flex-wrap">
            <span className="text-sm font-bold text-foreground leading-snug group-hover:text-primary transition-colors">
              {finding.title}
            </span>
            <span className={`text-[10px] font-black px-2 py-0.5 rounded-md uppercase tracking-widest ${s.badge}`}>
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
        <div className="px-6 pb-6 space-y-6 border-t border-border/10 pt-5">
          <ParsedDescription text={finding.description} />

          {finding.suggestion && (
            <div className="space-y-3">
              <div className="flex items-center gap-2 text-xs font-semibold text-foreground/50 uppercase tracking-widest px-1">
                <Lightbulb className="h-3 w-3 text-primary" />
                Suggested Fix
              </div>
              <CodeSuggestion code={finding.suggestion} />
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
  severity: 'critical' | 'high' | 'medium' | 'low'
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

// ── History Component ─────────────────────────────────────────────────────────

function ReviewHistory({ 
  history, 
  currentId, 
  repoId, 
  prNum 
}: { 
  history: any[]
  currentId: number
  repoId: string
  prNum: string
}) {
  return (
    <div className="flex items-center gap-2 overflow-x-auto pb-2 scrollbar-none">
      {history.map((item) => {
        const active = item.id === currentId
        return (
          <Link
            key={item.id}
            to={`/repos/${repoId}/reviews/${prNum}?id=${item.id}`}
            className={`
              flex flex-col gap-1 px-4 py-2 rounded-xl border transition-all shrink-0
              ${active 
                ? 'bg-primary/10 border-primary/30 ring-1 ring-primary/20' 
                : 'bg-card border-border/40 hover:border-border hover:bg-accent/20'}
            `}
          >
            <div className="flex items-center justify-between gap-4">
              <span className={`text-[10px] font-black uppercase tracking-widest ${active ? 'text-primary' : 'text-muted-foreground'}`}>
                V{item.revision} {item.is_latest && '(Latest)'}
              </span>
              {item.total_critical > 0 && (
                <span className="h-1.5 w-1.5 rounded-full bg-rose-500 shadow-[0_0_8px_rgba(244,63,94,0.6)]" />
              )}
            </div>
            <span className="text-[10px] text-muted-foreground/60 font-mono">
              {new Date(item.created_at).toLocaleDateString(undefined, { day: 'numeric', month: 'short' })}
            </span>
          </Link>
        )
      })}
    </div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

export default function ReviewDetailPage() {
  const { repoId, prNum } = useParams<{ repoId: string; prNum: string }>()
  const location = useLocation()
  const searchParams = new URLSearchParams(location.search)
  const specificId = searchParams.get('id')
  
  const id = parseInt(repoId ?? '0', 10)
  const prNumber = parseInt(prNum ?? '0', 10)

  const { data: review, isLoading } = useQuery<ReviewDetail>({
    queryKey: ['review', id, prNumber, specificId],
    queryFn: () => api.reviews.get(id, prNumber, specificId ? parseInt(specificId) : undefined),
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

  const findings = review.findings ?? []
  const criticals = findings.filter(f => f.severity === 'critical')
  const highs      = findings.filter(f => f.severity === 'high')
  const mediums    = findings.filter(f => f.severity === 'medium')
  const lows       = findings.filter(f => f.severity === 'low')

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
            <h1 className="text-2xl font-bold text-foreground tracking-tight flex items-center gap-3">
              {review.pr_title}
              {review.history && review.history.length > 1 && (
                <Badge variant="outline" className="bg-blue-500/10 text-blue-400 border-blue-500/20 px-2 py-0.5 text-[10px] font-black uppercase tracking-widest h-fit">
                  V{review.revision}
                </Badge>
              )}
            </h1>
          </div>
        </div>

        {/* Revision switcher if there is history */}
        {review.history && review.history.length > 1 && (
          <div className="pt-2">
            <p className="text-[10px] font-bold text-muted-foreground uppercase tracking-widest mb-3 flex items-center gap-2">
              <RefreshCw className="h-3 w-3" />
              Review History
            </p>
            <ReviewHistory 
              history={review.history} 
              currentId={review.id} 
              repoId={repoId!} 
              prNum={prNum!} 
            />
          </div>
        )}

        {/* Severity summary */}
        <div className="flex items-center gap-2 flex-wrap">
          <Badge variant="outline" className={`bg-rose-500/10 text-rose-400 border-rose-500/20 px-2.5 py-1 text-xs font-bold`}>
            {criticals.length} critical
          </Badge>
          <Badge variant="outline" className={`bg-orange-500/10 text-orange-400 border-orange-500/20 px-2.5 py-1 text-xs font-bold`}>
            {highs.length} high
          </Badge>
          <Badge variant="outline" className={`bg-amber-500/10 text-amber-400 border-amber-500/20 px-2.5 py-1 text-xs font-bold`}>
            {mediums.length} medium
          </Badge>
          <Badge variant="outline" className={`bg-emerald-500/10 text-emerald-400 border-emerald-500/20 px-2.5 py-1 text-xs font-bold`}>
            {lows.length} low
          </Badge>
          <span className="text-xs text-muted-foreground ml-1">
            {findings.length} total finding{findings.length !== 1 ? 's' : ''}
          </span>
        </div>
      </motion.div>

      {/* Findings grouped by severity */}
      <motion.div variants={stagger} className="space-y-6">
        <FindingGroup severity="critical" findings={criticals} repoId={repoId!} prNum={prNum!} />
        <FindingGroup severity="high"     findings={highs}      repoId={repoId!} prNum={prNum!} />
        <FindingGroup severity="medium"   findings={mediums}    repoId={repoId!} prNum={prNum!} />
        <FindingGroup severity="low"      findings={lows}       repoId={repoId!} prNum={prNum!} />
      </motion.div>
    </motion.div>
  )
}
