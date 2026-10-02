export function Brand({ collapsed = false }: { collapsed?: boolean }) {
  return (
    <div className={collapsed ? 'flex justify-center py-5' : 'flex items-center gap-2.5 px-3 py-5'}>
      <div className="flex h-6 w-6 items-center justify-center rounded-lg bg-gradient-to-br from-ink to-ink/70 text-[12px] font-semibold text-white">P</div>
      {!collapsed && (
        <div>
          <div className="text-[13px] font-semibold tracking-tight text-ink">PlatePilot</div>
          <div className="text-[10px] text-ink-tertiary">Admin console</div>
        </div>
      )}
    </div>
  )
}