import type { ReviewSummary } from './api'

export interface GroupedReview {
  pr_number: number
  pr_title: string
  /** The original (V1 / lowest revision) review — shown as the parent row */
  original_review: ReviewSummary
  /** Latest revision — used for showing current severity state */
  latest_review: ReviewSummary
  /** All subsequent revisions (V2, V3…), sorted by revision ASC */
  revisions: ReviewSummary[]
}

export function groupReviews(reviews: ReviewSummary[]): GroupedReview[] {
  const groups: Record<number, GroupedReview> = {}

  for (const review of reviews) {
    if (!groups[review.pr_number]) {
      groups[review.pr_number] = {
        pr_number: review.pr_number,
        pr_title: review.pr_title,
        original_review: review,
        latest_review: review,
        revisions: [],
      }
    } else {
      const group = groups[review.pr_number]

      // Track the oldest (lowest revision) as the parent
      if (review.revision < group.original_review.revision) {
        // The current review is older — demote the existing original to a revision
        group.revisions.push(group.original_review)
        group.original_review = review
      } else {
        group.revisions.push(review)
      }

      // Always keep track of the latest revision
      if (review.revision > group.latest_review.revision) {
        group.latest_review = review
      }
    }
  }

  // Sort revisions within each group ascending (V2, V3…)
  Object.values(groups).forEach(group => {
    group.revisions.sort((a, b) => a.revision - b.revision)
  })

  // Sort groups by most recent activity (latest revision date) descending
  return Object.values(groups).sort((a, b) =>
    new Date(b.latest_review.reviewed_at).getTime() - new Date(a.latest_review.reviewed_at).getTime()
  )
}
