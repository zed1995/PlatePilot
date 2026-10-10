import { NavLink } from 'react-router-dom'
import { LayoutGrid, Store, FileText, ScrollText, UploadCloud, FlaskConical, MessagesSquare, CalendarClock } from 'lucide-react'
import { Brand } from './brand'
import { cn } from '../lib/utils'

// Two groups, because the app now has two postures. The ops pages read the
// database through /admin/v1 and change nothing; the verification pages act:
// the Agent Console talks to /v1 as a user, and the inventory page's reset
// returns the mock demo state to pristine. Listing them as one flat set would
// quietly retire the "read-only console" claim that web/AGENTS.md is built on.
const groups = [
  {
    label: '运维台',
    items: [
      { to: '/dashboard', label: 'Dashboard', icon: LayoutGrid },
      { to: '/restaurants', label: 'Restaurants', icon: Store },
      { to: '/documents', label: 'Documents', icon: FileText },
      { to: '/digests', label: 'Review Digests', icon: ScrollText },
      { to: '/ingestion', label: 'Ingestion', icon: UploadCloud },
      { to: '/retrieval-debug', label: 'Retrieval Debug', icon: FlaskConical },
    ],
  },
  {
    label: '验证台',
    items: [
      { to: '/inventory', label: 'Mock 库存', icon: CalendarClock },
      { to: '/agent', label: 'Agent Console', icon: MessagesSquare },
    ],
  },
]

const items = groups.flatMap((group) => group.items)

function matchKey(pathname: string) {
  return items.find((i) => pathname.startsWith(i.to))?.to ?? pathname
}

export function Sidebar() {
  const pathname = typeof window !== 'undefined' ? window.location.pathname : '/'
  const active = matchKey(pathname)
  return (
    <aside className="sticky top-0 flex h-screen w-[220px] flex-col border-r border-[var(--border-subtle)] bg-surface backdrop-blur-xl">
      <Brand />
      <nav className="flex-1 space-y-3 px-2.5">
        {groups.map((group) => (
          <div key={group.label} className="space-y-0.5">
            <p className="m-0 px-3 pb-1 text-[10px] font-medium uppercase tracking-[0.06em] text-ink-tertiary">
              {group.label}
            </p>
            {group.items.map(({ to, label, icon: Icon }) => (
              <NavLink
                key={to}
                to={to}
                className={({ isActive }) =>
                  cn(
                    'flex h-9 items-center gap-2.5 rounded-lg px-3 text-[13px] transition-colors',
                    isActive || active === to
                      ? 'bg-black/[0.05] font-medium text-ink'
                      : 'text-ink-secondary hover:bg-black/[0.03]',
                  )
                }
              >
                <Icon className="h-4 w-4" />
                {label}
              </NavLink>
            ))}
          </div>
        ))}
      </nav>
    </aside>
  )
}
