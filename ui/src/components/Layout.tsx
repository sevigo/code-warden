import { NavLink, useNavigate, Link } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Shield, Search, Settings, Plus, Loader2, GitBranch, MessageSquare } from 'lucide-react'
import { cn } from '@/lib/utils'
import { api } from '@/lib/api'
import type { Repository, ScanState } from '@/lib/api'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'

function RepoStatusDot({ repoId }: { repoId: number }) {
  const { data: scanState } = useQuery<ScanState | null>({
    queryKey: ['scanState', repoId],
    queryFn: () => api.repos.status(repoId),
    refetchInterval: (query) => {
      const s = query.state.data?.status
      return s === 'scanning' || s === 'in_progress' || s === 'pending' ? 2000 : false
    },
  })

  const status = scanState?.status
  if (!status) return <div className="h-2 w-2 rounded-full bg-zinc-600" title="Not indexed" />
  if (status === 'completed') return <div className="h-2 w-2 rounded-full bg-emerald-400" title="Ready" />
  if (status === 'failed') return <div className="h-2 w-2 rounded-full bg-red-400" title="Failed" />
  return <div className="h-2 w-2 rounded-full bg-blue-400 animate-subtle-pulse" title="Indexing" />
}

export default function Layout({ children, fluid }: { children: React.ReactNode; fluid?: boolean }) {
  const [search, setSearch] = useState('')
  const [showAdd, setShowAdd] = useState(false)
  const [name, setName] = useState('')
  const [formError, setFormError] = useState('')
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const { data: repos, isLoading } = useQuery<Repository[]>({
    queryKey: ['repos'],
    queryFn: api.repos.list,
  })

  const addRepo = useMutation({
    mutationFn: () => {
      // Extract owner/repo from GitHub URL if needed
      let repoName = name.trim()
      if (repoName.startsWith('http') || repoName.startsWith('github.com')) {
        const match = repoName.match(/github\.com\/([^/]+\/[^/]+)/)
        if (match) {
          repoName = match[1]
        }
      }
      return api.repos.register({ full_name: repoName })
    },
    onSuccess: (newRepo) => {
      queryClient.invalidateQueries({ queryKey: ['repos'] })
      setShowAdd(false)
      setName('')
      setFormError('')
      navigate(`/repos/${newRepo.id}`)
    },
    onError: (err) => {
      setFormError(err instanceof Error ? err.message : 'Failed to add repository')
    },
  })

  const handleAddSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    setFormError('')
    if (!name.trim()) { setFormError('Repository name is required'); return }
    
    // Check if it's a GitHub URL and extract owner/repo
    const trimmed = name.trim()
    if (trimmed.startsWith('http') || trimmed.startsWith('github.com')) {
      const match = trimmed.match(/github\.com\/([^/]+\/[^/]+)/)
      if (!match) {
        setFormError('Invalid GitHub URL format')
        return
      }
    } else if (!trimmed.includes('/') || trimmed.split('/').length !== 2) {
      setFormError('Use "owner/repo" format (e.g., sevigo/karakuri-os)')
      return
    }
    
    addRepo.mutate()
  }

  const handleDialogChange = (open: boolean) => {
    setShowAdd(open)
    if (!open) { setName(''); setFormError('') }
  }

  const filtered = repos?.filter((r) =>
    !search || r.full_name.toLowerCase().includes(search.toLowerCase())
  )

  return (
    <div className="flex h-screen bg-background">
      {/* ── Sidebar ─────────────────────────────────────── */}
      <aside className="w-72 shrink-0 flex flex-col bg-surface border-r border-border/50">
        {/* Branding */}
        <div className="relative px-5 py-5 overflow-hidden">
          <div className="absolute -top-10 -left-10 h-32 w-32 rounded-full bg-primary/8 blur-3xl pointer-events-none" />
          <div className="relative flex items-center gap-3">
            <div className="h-9 w-9 rounded-xl bg-primary flex items-center justify-center shrink-0 shadow-lg shadow-primary/25">
              <Shield className="h-5 w-5 text-primary-foreground" />
            </div>
            <div>
              <span className="font-semibold text-foreground text-sm leading-none block">Code Warden</span>
              <span className="text-[11px] text-muted-foreground leading-none mt-0.5 block">AI Code Intelligence</span>
            </div>
          </div>
        </div>

        {/* Overview Nav */}
        <div className="px-3 pb-2">
          <NavLink
            to="/"
            end
            className={({ isActive }) => cn(
              'flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm transition-all duration-200',
              isActive
                ? 'bg-primary/10 text-primary font-medium'
                : 'text-muted-foreground hover:text-foreground hover:bg-accent/50'
            )}
          >
            <GitBranch className="h-4 w-4 shrink-0" />
            Overview
          </NavLink>
        </div>

        {/* Repos section */}
        <div className="flex-1 flex flex-col min-h-0 px-3">
          <div className="flex items-center justify-between px-1 mb-2">
            <p className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">
              Repositories
            </p>
            <button
              onClick={() => setShowAdd(true)}
              className="p-1 rounded-md hover:bg-accent/50 text-muted-foreground hover:text-foreground transition-colors"
              title="Add repository"
            >
              <Plus className="h-3.5 w-3.5" />
            </button>
          </div>

          {/* Search */}
          {repos && repos.length > 2 && (
            <div className="relative mb-2">
              <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
              <input
                type="text"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search..."
                aria-label="Search repositories"
                className="w-full pl-8 pr-3 py-1.5 rounded-md bg-accent/30 text-foreground text-xs focus:outline-none focus:ring-1 focus:ring-primary/30 placeholder:text-muted-foreground/50 transition-all"
              />
            </div>
          )}

          {/* Repo list */}
          <nav className="flex-1 overflow-y-auto space-y-0.5 pb-3 -mx-1 px-1">
            {isLoading ? (
              <div className="flex flex-col items-center py-8 text-muted-foreground gap-2">
                <Loader2 className="h-4 w-4 animate-spin" />
                <span className="text-xs">Loading...</span>
              </div>
            ) : filtered && filtered.length > 0 ? (
              filtered.map((repo) => {
                const [, repoName] = repo.full_name.split('/')
                return (
                  <div key={repo.id} className="relative group">
                    <NavLink
                      to={`/repos/${repo.id}`}
                      className={({ isActive }) => cn(
                        'flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm transition-all duration-150',
                        isActive
                          ? 'bg-accent text-foreground font-medium'
                          : 'text-muted-foreground hover:text-foreground hover:bg-accent/40'
                      )}
                    >
                      <RepoStatusDot repoId={repo.id} />
                      <span className="truncate flex-1">{repoName}</span>
                    </NavLink>
                    <Link
                      to={`/repos/${repo.id}/chat`}
                      className="absolute right-2 top-1/2 -translate-y-1/2 p-1 rounded opacity-0 group-hover:opacity-100 hover:bg-primary/10 text-muted-foreground hover:text-primary transition-all"
                      title="Chat with AI"
                    >
                      <MessageSquare className="h-3 w-3" />
                    </Link>
                  </div>
                )
              })
            ) : repos && repos.length > 0 ? (
              <p className="text-xs text-muted-foreground/50 text-center py-4">No match</p>
            ) : (
              <button
                onClick={() => setShowAdd(true)}
                className="flex flex-col items-center gap-2 py-8 text-muted-foreground hover:text-foreground transition-colors w-full"
              >
                <div className="h-10 w-10 rounded-xl bg-primary/10 flex items-center justify-center">
                  <Plus className="h-5 w-5 text-primary" />
                </div>
                <span className="text-xs">Add your first repo</span>
              </button>
            )}
          </nav>
        </div>

        {/* Footer */}
        <div className="px-3 py-3 border-t border-border/30">
          <button
            disabled
            title="Coming soon"
            className="flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-muted-foreground/50 w-full cursor-not-allowed"
          >
            <Settings className="h-4 w-4 shrink-0" />
            Settings
          </button>
        </div>
      </aside>

      {/* ── Main Canvas ─────────────────────────────────── */}
      <main className="flex-1 overflow-hidden flex flex-col">
        <div className={cn(
          "flex-1 overflow-auto",
          !fluid && "px-10 py-8 max-w-[1200px] mx-auto w-full"
        )}>
          {children}
        </div>
      </main>

      {/* ── Add Repo Dialog ─────────────────────────────── */}
      <Dialog open={showAdd} onOpenChange={handleDialogChange}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Add Repository</DialogTitle>
            <p className="text-sm text-muted-foreground">
              Enter a repository name in owner/repo format, or paste a GitHub URL.
            </p>
          </DialogHeader>
          <form onSubmit={handleAddSubmit} className="space-y-4 pt-2">
            <div>
              <label className="text-sm font-medium mb-1.5 block text-foreground">Repository Name</label>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="owner/repo or paste GitHub URL"
                aria-label="Repository name"
                className="w-full px-3 py-2.5 rounded-lg bg-accent/30 text-foreground focus:outline-none focus:ring-2 focus:ring-primary/30 text-sm placeholder:text-muted-foreground/50 transition-all"
                autoFocus
              />
              <p className="text-xs text-muted-foreground mt-1">e.g., sevigo/karakuri-os</p>
            </div>
            {formError && (
              <p className="text-sm text-red-400 bg-red-500/10 rounded-lg px-3 py-2.5">{formError}</p>
            )}
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={() => handleDialogChange(false)}>Cancel</Button>
              <Button type="submit" disabled={!name || addRepo.isPending}>
                {addRepo.isPending ? <><Loader2 className="h-4 w-4 mr-2 animate-spin" />Adding...</> : 'Add Repository'}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  )
}
