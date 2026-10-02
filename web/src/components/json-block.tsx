export function JsonBlock({ value }: { value: unknown }) {
  const text =
    value === null || value === undefined
      ? ''
      : typeof value === 'string'
        ? value
        : JSON.stringify(value, null, 2)
  return (
    <pre className="m-0 max-h-[400px] overflow-auto rounded-lg border border-[var(--border-subtle)] bg-black/[0.02] p-3.5 text-[12px] font-mono text-ink">
      {text}
    </pre>
  )
}