import { NavLink } from 'react-router-dom'
import { Shield, GitBranch, Settings } from 'lucide-react'
import { cn } from '@/lib/utils'

export default function Layout({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-screen bg-background">
      <aside className="w-64 shrink-0 flex flex-col bg-zinc-950/80 border-r border-zinc-800/70">
        {/* Logo — gradient accent behind */}
        <div className="relative flex items-center gap-3 px-5 py-5 border-b border-zinc-800/70 overflow-hidden">
          {/* subtle radial glow behind logo */}
          <div className="absolute -top-6 -left-6 h-24 w-24 rounded-full bg-primary/10 blur-2xl pointer-events-none" />
          <div className="relative h-8 w-8 rounded-lg bg-primary flex items-center justify-center shrink-0 shadow-lg shadow-primary/20">
            <Shield className="h-4.5 w-4.5 text-primary-foreground" />
          </div>
          <div className="relative">
            <span className="font-semibold text-zinc-100 text-sm leading-none block">Code Warden</span>
            <span className="text-[11px] text-zinc-500 leading-none mt-0.5 block">AI Code Intelligence</span>
          </div>
        </div>

        {/* Nav */}
        <nav className="flex-1 p-3 space-y-0.5">
          <p className="text-[10px] font-medium text-zinc-600 uppercase tracking-wider px-3 py-2">Navigation</p>
          <NavLink
            to="/"
            end
            className={({ isActive }) => cn(
              'flex items-center gap-2.5 px-3 py-2 rounded-md text-sm transition-all duration-200',
              isActive
                ? 'bg-primary/10 text-primary font-medium border border-primary/20'
                : 'text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800/60'
            )}
          >
            <GitBranch className="h-4 w-4 shrink-0" />
            Repositories
          </NavLink>
        </nav>

        {/* Footer */}
        <div className="p-3 border-t border-zinc-800/70">
          <button
            disabled
            title="Coming soon"
            className="flex items-center gap-2.5 px-3 py-2 rounded-md text-sm text-zinc-600 w-full cursor-not-allowed hover:text-zinc-500 transition-colors"
          >
            <Settings className="h-4 w-4 shrink-0" />
            Settings
          </button>
        </div>
      </aside>

      <main className="flex-1 overflow-auto">
        <div className="px-8 py-8 max-w-[1400px] mx-auto">
          {children}
        </div>
      </main>
    </div>
  )
}
