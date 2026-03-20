import { NavLink } from 'react-router-dom'
import { Shield, GitBranch, Settings } from 'lucide-react'
import { cn } from '@/lib/utils'

export default function Layout({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-screen bg-background">
      <aside className="w-60 shrink-0 flex flex-col bg-zinc-900 border-r border-zinc-800">
        {/* Logo */}
        <div className="flex items-center gap-2.5 px-4 py-4 border-b border-zinc-800">
          <div className="h-7 w-7 rounded-md bg-primary flex items-center justify-center">
            <Shield className="h-4 w-4 text-primary-foreground" />
          </div>
          <span className="font-semibold text-zinc-100 text-sm">Code Warden</span>
        </div>
        {/* Nav */}
        <nav className="flex-1 p-2 space-y-0.5">
          <NavLink
            to="/"
            end
            className={({ isActive }) => cn(
              'flex items-center gap-2.5 px-3 py-2 rounded-md text-sm transition-colors',
              isActive
                ? 'bg-zinc-700 text-zinc-100'
                : 'text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800'
            )}
          >
            <GitBranch className="h-4 w-4" />
            Repositories
          </NavLink>
        </nav>
        {/* Footer */}
        <div className="p-2 border-t border-zinc-800">
          <button className="flex items-center gap-2.5 px-3 py-2 rounded-md text-sm text-zinc-500 w-full cursor-not-allowed">
            <Settings className="h-4 w-4" />
            Settings
          </button>
        </div>
      </aside>
      <main className="flex-1 overflow-auto">
        <div className="p-6 max-w-5xl mx-auto">
          {children}
        </div>
      </main>
    </div>
  )
}
