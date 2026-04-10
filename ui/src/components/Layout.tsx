import { NavLink, useNavigate, useLocation, Link } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState, useCallback } from 'react'
import {
  Shield, Search, Plus, Loader2, MessageSquare,
  LayoutDashboard, Activity, Settings, Sun, Moon, X
} from 'lucide-react'
import { Toaster } from 'sonner'
import { motion, AnimatePresence } from 'framer-motion'
import { cn } from '@/lib/utils'
import { api } from '@/lib/api'
import type { Repository, SetupStatus } from '@/lib/api'
import { StatusDot } from '@/components/StatusBadge'
import { useTheme } from '@/lib/useTheme'
import { Button } from '@/components/ui/button'

/**
 * HashiCorp-Inspired Layout Component
 * 
 * Features:
 * - Dual-mode sidebar (light content area, dark sidebar option)
 * - Clean navigation with status indicators
 * - Organized repository list with search
 * - Minimal visual noise with micro-shadows
 * - Systematic spacing (8px base unit)
 */

interface SidebarProps {
  children: React.ReactNode
  fluid?: boolean
}

// Nav item styling following HashiCorp patterns
const navLinkClass = ({ isActive }: { isActive: boolean }) =>
  cn(
    'flex items-center gap-2.5 px-3 py-2 rounded-[5px] text-sm transition-all duration-150',
    isActive
      ? 'bg-primary/10 text-primary font-medium'
      : 'text-[#656a76] hover:text-foreground hover:bg-[#f1f2f3]',
    'dark:text-[#b2b6bd] dark:hover:bg-[#2d2f36] dark:hover:text-[#efeff1]'
  )

// Repository list item styling
const repoLinkClass = ({ isActive }: { isActive: boolean }) =>
  cn(
    'flex items-center gap-2.5 px-3 py-2 rounded-[5px] text-sm transition-all duration-150',
    isActive
      ? 'bg-accent text-foreground font-medium'
      : 'text-[#656a76] hover:text-foreground hover:bg-[#f1f2f3]',
    'dark:text-[#b2b6bd] dark:hover:bg-[#2d2f36] dark:hover:text-[#efeff1]'
  )

export default function Layout({ children }: SidebarProps) {
  const [search, setSearch] = useState('')
  const [showAddDialog, setShowAddDialog] = useState(false)
  const [repoName, setRepoName] = useState('')
  const [formError, setFormError] = useState('')
  const navigate = useNavigate()
  const location = useLocation()
  const queryClient = useQueryClient()
  const { theme, toggle } = useTheme()

  const parseGitHubRepo = useCallback((input: string): string | null => {
    const value = input.trim()
    const isUrlLike =
      value.startsWith('http://') ||
      value.startsWith('https://') ||
      value.startsWith('github.com/')

    if (!isUrlLike) return null

    try {
      const normalized = value.startsWith('github.com/') ? `https://${value}` : value
      const url = new URL(normalized)

      if (url.hostname !== 'github.com') return null

      const parts = url.pathname.split('/').filter(Boolean)
      if (parts.length < 2) return null

      return `${parts[0]}/${parts[1]}`
    } catch {
      return null
    }
  }, [])

  // Setup status check
  const { data: setupStatus } = useQuery<SetupStatus>({
    queryKey: ['setup-status'],
    queryFn: api.setup.status,
    retry: false,
    staleTime: 60_000,
  })

  useEffect(() => {
    if (setupStatus && !setupStatus.ready && location.pathname !== '/setup') {
      navigate('/setup', { replace: true })
    }
  }, [setupStatus, location.pathname, navigate])

  // Fetch repositories
  const { data: repos, isLoading: reposLoading } = useQuery<Repository[]>({
    queryKey: ['repos'],
    queryFn: api.repos.list,
  })

  // Add repository mutation
  const addRepo = useMutation({
    mutationFn: () => {
      let name = repoName.trim()
      const parsed = parseGitHubRepo(name)
      if (parsed) name = parsed
      return api.repos.register({ full_name: name })
    },
    onSuccess: (newRepo) => {
      queryClient.invalidateQueries({ queryKey: ['repos'] })
      setShowAddDialog(false)
      setRepoName('')
      setFormError('')
      navigate(`/repos/${newRepo.id}`)
    },
    onError: (err) => {
      setFormError(err instanceof Error ? err.message : 'Failed to add repository')
    },
  })

  const handleAddSubmit = useCallback((e: React.FormEvent) => {
    e.preventDefault()
    setFormError('')
    
    const trimmed = repoName.trim()
    if (!trimmed) {
      setFormError('Repository name is required')
      return
    }
    
    const parsed = parseGitHubRepo(trimmed)
    const isUrlLike =
      trimmed.startsWith('http://') ||
      trimmed.startsWith('https://') ||
      trimmed.startsWith('github.com/')

    if (isUrlLike) {
      if (!parsed) {
        setFormError('Invalid GitHub URL format')
        return
      }
    } else if (!trimmed.includes('/') || trimmed.split('/').length !== 2) {
      setFormError('Use "owner/repo" format (e.g., sevigo/code-warden)')
      return
    }
    
    addRepo.mutate()
  }, [repoName, addRepo])

  const filteredRepos = repos?.filter((r) =>
    !search || r.full_name.toLowerCase().includes(search.toLowerCase())
  )

  return (
    <div className="flex h-screen bg-[#f1f2f3] dark:bg-[#0d0e12]">
      <Toaster position="bottom-right" richColors />
      
      {/* ── Sidebar ──────────────────────────────────────────────────── */}
      <aside className="w-72 shrink-0 flex flex-col bg-white border-r border-[#d5d7db] dark:bg-[#15181e] dark:border-[#2d2f36]">
        {/* Brand Header */}
        <div className="px-5 py-5 border-b border-[#e1e3e6] dark:border-[#2d2f36]">
          <div className="flex items-center gap-3">
            <div className="h-8 w-8 rounded-[6px] bg-[#2264d6] flex items-center justify-center shrink-0">
              <Shield className="h-4 w-4 text-white" />
            </div>
            <div>
              <span className="font-semibold text-[#15181e] text-sm leading-none block dark:text-[#efeff1]">
                Code Warden
              </span>
              <span className="text-xs text-[#656a76] leading-none mt-1 block dark:text-[#b2b6bd]">
                AI Code Reviews
              </span>
            </div>
          </div>
        </div>

        {/* Main Navigation */}
        <nav className="px-3 py-3 space-y-0.5">
          <NavLink to="/" end className={navLinkClass}>
            <LayoutDashboard className="h-4 w-4 shrink-0" />
            Dashboard
          </NavLink>
          <NavLink to="/jobs" className={navLinkClass}>
            <Activity className="h-4 w-4 shrink-0" />
            Activity
          </NavLink>
          <NavLink to="/settings" className={navLinkClass}>
            <Settings className="h-4 w-4 shrink-0" />
            Settings
          </NavLink>
        </nav>

        <div className="px-4 py-2">
          <div className="h-px bg-[#e1e3e6] dark:bg-[#2d2f36]" />
        </div>

        {/* Repositories Section */}
        <div className="flex-1 flex flex-col min-h-0 px-3">
          <div className="flex items-center justify-between px-1 mb-2">
            <span className="text-xs font-semibold text-[#656a76] uppercase tracking-wider dark:text-[#b2b6bd]">
              Repositories
            </span>
            <button
              onClick={() => setShowAddDialog(true)}
              className="p-1.5 rounded-[4px] hover:bg-[#f1f2f3] text-[#656a76] hover:text-foreground transition-colors dark:hover:bg-[#2d2f36]"
              title="Add repository"
            >
              <Plus className="h-3.5 w-3.5" />
            </button>
          </div>

          {/* Search */}
          {repos && repos.length > 2 && (
            <div className="relative mb-2">
              <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-[#8c919b]" />
              <input
                type="text"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Filter repositories..."
                aria-label="Filter repositories"
                className="w-full pl-8 pr-3 py-1.5 rounded-[4px] bg-[#f1f2f3] text-sm focus:outline-none focus:ring-1 focus:ring-[#2264d6]/30 placeholder:text-[#8c9199] dark:bg-[#1e2025] dark:text-[#efeff1]"
              />
            </div>
          )}

          {/* Repository List */}
          <nav className="flex-1 overflow-y-auto space-y-0.5 pb-3 -mx-1 px-1">
            {reposLoading ? (
              <div className="flex flex-col items-center py-8 text-[#8c919b] gap-2">
                <Loader2 className="h-4 w-4 animate-spin" />
                <span className="text-xs">Loading...</span>
              </div>
            ) : filteredRepos && filteredRepos.length > 0 ? (
              filteredRepos.map((repo) => {
                const [, name] = repo.full_name.split('/')
                return (
                  <div key={repo.id} className="relative group">
                    <NavLink
                      to={`/repos/${repo.id}`}
                      className={repoLinkClass}
                    >
                      <StatusDot status={null} />
                      <span className="truncate flex-1">{name}</span>
                    </NavLink>
                    <Link
                      to={`/repos/${repo.id}/chat`}
                      className="absolute right-2 top-1/2 -translate-y-1/2 p-1.5 rounded-[4px] opacity-0 group-hover:opacity-100 hover:bg-[#2264d6]/10 text-[#656a76] hover:text-[#2264d6] transition-all"
                      title="Chat with AI"
                    >
                      <MessageSquare className="h-3 w-3" />
                    </Link>
                  </div>
                )
              })
            ) : repos && repos.length > 0 ? (
              <p className="text-xs text-[#8c919b] text-center py-4">No match</p>
            ) : (
              <button
                onClick={() => setShowAddDialog(true)}
                className="flex flex-col items-center gap-2 py-8 text-[#656a76] hover:text-foreground transition-colors w-full"
              >
                <div className="h-10 w-10 rounded-[6px] bg-[#2264d6]/10 flex items-center justify-center">
                  <Plus className="h-5 w-5 text-[#2264d6]" />
                </div>
                <span className="text-xs">Add your first repo</span>
              </button>
            )}
          </nav>
        </div>

        {/* Footer - Theme Toggle */}
        <div className="px-4 py-3 border-t border-[#e1e3e6] flex items-center justify-between dark:border-[#2d2f36]">
          <span className="text-xs text-[#656a76] dark:text-[#b2b6bd]">
            {theme === 'dark' ? 'Dark mode' : 'Light mode'}
          </span>
          <button
            onClick={toggle}
            title={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
            className="p-2 rounded-[5px] hover:bg-[#f1f2f3] text-[#656a76] hover:text-foreground transition-colors dark:hover:bg-[#2d2f36] dark:text-[#b2b6bd]"
          >
            {theme === 'dark'
              ? <Sun className="h-4 w-4" />
              : <Moon className="h-4 w-4" />}
          </button>
        </div>
      </aside>

      {/* ── Main Content ───────────────────────────────────────────────── */}
      <main className="flex-1 overflow-hidden flex flex-col bg-[#f1f2f3] dark:bg-[#0d0e12]">
        <div className="flex-1 overflow-auto px-8 py-6 max-w-[1200px] mx-auto w-full">
          {children}
        </div>
      </main>

      {/* ── Add Repository Dialog ────────────────────────────────────── */}
      <AnimatePresence>
        {showAddDialog && (
          <>
            <div 
              className="fixed inset-0 bg-black/30 z-40"
              onClick={() => setShowAddDialog(false)}
            />
            <motion.div
              initial={{ opacity: 0, scale: 0.95, y: -10 }}
              animate={{ opacity: 1, scale: 1, y: 0 }}
              exit={{ opacity: 0, scale: 0.95, y: -10 }}
              transition={{ duration: 0.15 }}
              className="fixed inset-0 z-50 flex items-center justify-center p-4 pointer-events-none"
            >
              <div className="bg-white dark:bg-[#15181e] rounded-[8px] shadow-[0_10px_15px_-3px_rgba(0,0,0,0.1)] border border-[#d5d7db] dark:border-[#2d2f36] w-full max-w-md p-6 pointer-events-auto">
                <div className="flex items-center justify-between mb-4">
                  <h2 className="text-lg font-semibold text-foreground">Add Repository</h2>
                  <button 
                    onClick={() => setShowAddDialog(false)}
                    className="p-1 rounded-[4px] hover:bg-[#f1f2f3] dark:hover:bg-[#2d2f36] transition-colors"
                  >
                    <X className="h-4 w-4 text-[#656a76]" />
                  </button>
                </div>
                
                <p className="text-sm text-[#656a76] mb-4">
                  Enter a repository name in owner/repo format, or paste a GitHub URL.
                </p>
                
                <form onSubmit={handleAddSubmit} className="space-y-4">
                  <div>
                    <label className="text-sm font-medium mb-1.5 block text-foreground">
                      Repository Name
                    </label>
                    <input
                      type="text"
                      value={repoName}
                      onChange={(e) => setRepoName(e.target.value)}
                      placeholder="owner/repo or paste GitHub URL"
                      aria-label="Repository name"
                      className="w-full px-3 py-2.5 rounded-[5px] bg-[#f1f2f3] text-foreground focus:outline-none focus:ring-1 focus:ring-[#2264d6]/30 text-sm placeholder:text-[#8c9199] dark:bg-[#1e2025]"
                      autoFocus
                    />
                    <p className="text-xs text-[#8c919b] mt-1">e.g., sevigo/code-warden</p>
                  </div>
                  
                  {formError && (
                    <div className="text-sm text-rose-500 bg-rose-500/10 rounded-[5px] px-3 py-2">
                      {formError}
                    </div>
                  )}
                  
                  <div className="flex justify-end gap-2 pt-2">
                    <Button 
                      type="button" 
                      variant="ghost" 
                      onClick={() => setShowAddDialog(false)}
                    >
                      Cancel
                    </Button>
                    
                    <Button 
                      type="submit" 
                      disabled={!repoName || addRepo.isPending}
                    >
                      {addRepo.isPending ? (
                        <>
                          <Loader2 className="h-4 w-4 mr-2 animate-spin" />
                          Adding...
                        </>
                      ) : (
                        'Add Repository'
                      )}
                    </Button>
                  </div>
                </form>
              </div>
            </motion.div>
          </>
        )}
      </AnimatePresence>
    </div>
  )
}
