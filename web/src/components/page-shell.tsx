import { AnimatePresence, motion } from 'motion/react'
import type { ReactNode } from 'react'
import { useLocation } from 'react-router-dom'
import { Sidebar } from './sidebar'

export function PageShell({ children }: { children: ReactNode }) {
  const location = useLocation()
  return (
    <div className="flex min-h-screen text-ink">
      <Sidebar />
      <main className="flex-1 px-8 py-7">
        <div className="mx-auto max-w-[1280px]">
          <AnimatePresence mode="wait">
            <motion.div
              key={location.pathname}
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              transition={{ duration: 0.1 }}
            >
              {children}
            </motion.div>
          </AnimatePresence>
        </div>
      </main>
    </div>
  )
}