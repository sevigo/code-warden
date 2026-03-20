import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Shield, Plus, Search } from 'lucide-react'
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
  const [search, setSearch] = useState('')

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

  const filtered = repos?.filter((r) =>
    search === '' || r.full_name.toLowerCase().includes(search.toLowerCase())
  )

  return (
    <div className="space-y-7">
      {/* Header */}
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-bold text-foreground">Repositories</h1>
          <p className="text-sm text-muted-foreground mt-1">
            Manage and explore your indexed codebases
          </p>
        </div>
        <Button onClick={() => setShowAdd(true)} className="shrink-0">
          <Plus className="h-4 w-4 mr-2" />
          Add Repository
        </Button>
      </div>

      {/* Search bar — only show when there are repos */}
      {repos && repos.length > 1 && (
        <div className="relative">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Filter repositories..."
            className="w-full pl-9 pr-4 py-2.5 rounded-lg border border-border bg-card text-foreground focus:outline-none focus:ring-2 focus:ring-primary text-sm placeholder:text-muted-foreground"
          />
        </div>
      )}

      {/* Content */}
      {isLoading ? (
        <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 gap-4">
          {[1, 2, 3, 4].map((i) => (
            <div key={i} className="h-48 bg-card rounded-xl border border-zinc-800 animate-pulse" />
          ))}
        </div>
      ) : filtered && filtered.length > 0 ? (
        <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 gap-4">
          {filtered.map((repo) => (
            <RepoCard
              key={repo.id}
              repo={repo}
              onScan={() => startScan.mutate(repo.id)}
            />
          ))}
        </div>
      ) : repos && repos.length > 0 ? (
        <div className="py-16 text-center text-muted-foreground text-sm">
          No repositories match your search.
        </div>
      ) : (
        /* Empty state */
        <div className="flex flex-col items-center justify-center py-20 text-center">
          <div className="h-16 w-16 rounded-2xl bg-primary/10 flex items-center justify-center mb-5">
            <Shield className="h-8 w-8 text-primary" />
          </div>
          <h2 className="text-xl font-semibold mb-2">No repositories yet</h2>
          <p className="text-muted-foreground text-sm mb-8 max-w-md">
            Add a local repository to start exploring your codebase with AI-powered insights.
          </p>
          <div className="text-left space-y-3 mb-8 w-full max-w-sm">
            {[
              'Add a local repository path',
              'Run the initial scan (5–30 min depending on size)',
              'Ask questions about your codebase',
            ].map((step, i) => (
              <div key={i} className="flex items-start gap-3">
                <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary text-primary-foreground text-xs font-semibold">
                  {i + 1}
                </span>
                <span className="text-sm text-muted-foreground pt-0.5">{step}</span>
              </div>
            ))}
          </div>
          <Button onClick={() => setShowAdd(true)}>
            <Plus className="h-4 w-4 mr-2" />
            Add your first repository
          </Button>
        </div>
      )}

      {/* Add Repository Dialog */}
      <Dialog open={showAdd} onOpenChange={handleDialogChange}>
        <DialogContent className="sm:max-w-md">
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
                className="w-full px-3 py-2.5 rounded-lg border border-border bg-background text-foreground focus:outline-none focus:ring-2 focus:ring-primary text-sm placeholder:text-muted-foreground"
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
                className="w-full px-3 py-2.5 rounded-lg border border-border bg-background text-foreground focus:outline-none focus:ring-2 focus:ring-primary text-sm placeholder:text-muted-foreground font-mono"
              />
            </div>
            {formError && (
              <p className="text-sm text-red-400 bg-red-500/10 rounded-lg px-3 py-2.5">{formError}</p>
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
