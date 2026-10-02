import { NavLink } from 'react-router-dom'
import { LayoutGrid, Store, FileText, UploadCloud, FlaskConical } from 'lucide-react'
import { Brand } from './brand'
import { cn } from '../lib/utils'

const items = [
  { to: '/dashboard', label: 'Dashboard', icon: LayoutGrid },
  { to: '/restaurants', label: 'Restaurants', icon: Store },
  { to: '/documents', label: 'Documents', icon: FileText },
  { to: '/ingestion', label: 'Ingestion', icon: UploadCloud },
  { to: '/retrieval-debug', label: 'Retrieval Debug', icon: FlaskConical },
]

function matchKey(pathname: string) {
  return items.find((i) => pathname.startsWith(i.to))?.to ?? pathname
}

export function Sidebar() {
  const pathname = typeof window !== 'undefined' ? window.location.pathname : '/'
  const active = matchKey(pathname)
  return (
    <aside className="sticky top-0 flex h-screen w-[220px] flex-col border-r border-[var(--border-subtle)] bg-surface backdrop-blur-xl">
      <Brand />
      <nav className="flex-1 space-y-0.5 px-2.5">
        {items.map(({ to, label, icon: Icon }) => (
          <NavLink
            key={to}
            to={to}
            className={({ isActive }) =>
              cn(
                'flex h-9 items-center gap-2.5 rounded-lg px-3 text-[13px] transition-colors',
                isActive || active === to ? 'bg-black/[0.05] font-medium text-ink' : 'text-ink-secondary hover:bg-black/[0.03]',
              )
            }
          >
            <Icon className="h-4 w-4" />
            {label}
          </NavLink>
        ))}
      </nav>
    </aside>
  )
}