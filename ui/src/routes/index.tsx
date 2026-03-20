import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Shield, Plus } from 'lucide-react'
import RepoCard from '@/components/RepoCard'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog'
import { api } from '@/lib/api'
import type { Repository } from '@/lib/api'

function Dashboard() {
  const queryClient = useQueryClient()
  const [showAdd, setShowAdd] = useState(false)
  const [name, setName] = useState('')
  const [path, setPath] = useState('')
  const [formError, setFormError] = useState('')

  const { data: repos, isLoading } = useQuery<Repository[]>({
    queryKey: ['repos'],
    queryFn: api.repos.list,
  })

  const startScan = useMutation({
    mutationFn: (repoId: number) => api.repos.scan(repoId),
    onSuccess: (_data, repoId) => {
      queryClient.invalidateQueries({ queryKey: ['scanState', repoId] })
    },
  })

  const addRepo = useMutation({
    mutationFn: () => api.repos.register({ clone_path: path, full_name: name }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['repos'] })
      setShowAdd(false)
      setName('')
      setPath('')
      setFormError('')
    },
    onError: (err) => {
      setFormError(err instanceof Error ? err.message : 'Failed to add repository')
    },
  })

  const handleAddSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    setFormError('')
    if (!name.trim() || !path.trim()) {
      setFormError('Both fields are required')
      return
    }
    if (!name.includes('/')) {
      setFormError('Repository name must be in "owner/repo" format')
      return
    }
    addRepo.mutate()
  }

  const handleDialogChange = (open: boolean) => {
    setShowAdd(open)
    if (!open) {
      setName('')
      setPath('')
      setFormError('')
    }
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold text-foreground">Repositories</h1>
        <Button onClick={() => setShowAdd(true)}>
          <Plus className="h-4 w-4 mr-2" />
          Add Repository
        </Button>
      </div>

      {/* Content */}
      {isLoading ? (
        <div className="space-y-3">
          {[1, 2, 3].map((i) => (
            <div key={i} className="h-20 bg-card rounded-lg border border-border animate-pulse" />
          ))}
        </div>
      ) : repos && repos.length > 0 ? (
        <div className="space-y-3">
          {repos.map((repo) => (
            <RepoCard
              key={repo.id}
              repo={repo}
              onScan={() => startScan.mutate(repo.id)}
            />
          ))}
        </div>
      ) : (
        /* Empty state */
        <div className="flex flex-col items-center justify-center py-20 text-center">
          <Shield className="h-12 w-12 text-muted-foreground mb-4" />
          <h2 className="text-lg font-semibold mb-1">No repositories yet</h2>
          <p className="text-muted-foreground text-sm mb-8 max-w-sm">
            Add a local repository to start exploring your codebase with AI-powered insights.
          </p>
          <div className="text-left space-y-3 mb-8 w-full max-w-sm">
            {[
              'Add a local repository path',
              'Run the initial scan (5–30 min depending on size)',
              'Ask questions about your codebase',
            ].map((step, i) => (
              <div key={i} className="flex items-start gap-3">
                <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary text-primary-foreground text-xs font-medium">
                  {i + 1}
                </span>
                <span className="text-sm text-muted-foreground pt-0.5">{step}</span>
              </div>
            ))}
          </div>
          <Button onClick={() => setShowAdd(true)}>Add your first repository</Button>
        </div>
      )}

      {/* Add Repository Dialog */}
      <Dialog open={showAdd} onOpenChange={handleDialogChange}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add Repository</DialogTitle>
          </DialogHeader>
          <form onSubmit={handleAddSubmit} className="space-y-4 pt-2">
            <div>
              <label className="text-sm font-medium mb-1.5 block text-foreground">
                Repository Name
              </label>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="owner/repo"
                className="w-full px-3 py-2 rounded-md border border-border bg-background text-foreground focus:outline-none focus:ring-2 focus:ring-primary text-sm"
                autoFocus
              />
            </div>
            <div>
              <label className="text-sm font-medium mb-1.5 block text-foreground">
                Local Path
              </label>
              <input
                type="text"
                value={path}
                onChange={(e) => setPath(e.target.value)}
                placeholder="/path/to/repository"
                className="w-full px-3 py-2 rounded-md border border-border bg-background text-foreground focus:outline-none focus:ring-2 focus:ring-primary text-sm"
              />
            </div>
            {formError && (
              <p className="text-sm text-red-400 bg-red-500/10 rounded-md px-3 py-2">{formError}</p>
            )}
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={() => handleDialogChange(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={!name || !path || addRepo.isPending}>
                {addRepo.isPending ? 'Adding...' : 'Add Repository'}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  )
}

export default Dashboard
