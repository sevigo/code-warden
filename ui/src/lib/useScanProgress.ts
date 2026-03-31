import { useEffect, useRef } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api } from './api'
import type { ScanState } from './api'

/**
 * Opens an SSE connection for scan progress of a given repo.
 * Invalidates relevant queries and fires toast notifications on completion.
 * The connection is closed when the scan reaches a terminal state or the component unmounts.
 */
export function useScanProgress(repoId: number | undefined) {
  const queryClient = useQueryClient()
  const esRef = useRef<EventSource | null>(null)

  useEffect(() => {
    if (!repoId) return

    // Don't open duplicate connections
    if (esRef.current) {
      esRef.current.close()
    }

    const es = api.events.scanProgress(repoId)
    esRef.current = es

    es.onmessage = (event) => {
      try {
        const state: ScanState = JSON.parse(event.data)

        // Push the new state into the query cache so the UI updates immediately
        queryClient.setQueryData(['scanState', repoId], state)

        if (state.status === 'completed') {
          toast.success('Scan complete', {
            description: `Repository is indexed and ready for reviews.`,
          })
          // Invalidate related queries so stats / reviews reload
          queryClient.invalidateQueries({ queryKey: ['repo', String(repoId)] })
          queryClient.invalidateQueries({ queryKey: ['stats', String(repoId)] })
          queryClient.invalidateQueries({ queryKey: ['global-stats'] })
          es.close()
        } else if (state.status === 'failed') {
          toast.error('Scan failed', {
            description: 'Check server logs for details.',
          })
          es.close()
        }
      } catch {
        // Ignore malformed SSE events
      }
    }

    es.onerror = () => {
      // SSE error — the browser will reconnect automatically; nothing to do
    }

    return () => {
      es.close()
      esRef.current = null
    }
  }, [repoId, queryClient])
}
