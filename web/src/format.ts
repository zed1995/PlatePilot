// format holds small display helpers shared across pages.

// formatTime renders an RFC3339 value as compact UTC text, or "-" when empty.
export function formatTime(value?: string): string {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toISOString().replace('T', ' ').replace(/\.\d+Z$/, 'Z')
}

// formatDuration turns milliseconds into a compact duration.
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const minutes = Math.floor(ms / 60_000)
  const seconds = Math.round((ms % 60_000) / 1000)
  return `${minutes}m ${seconds}s`
}

// statusColor maps a batch status onto an AntD tag color. Unknown values use a
// neutral color so a new status never renders as success.
export function statusColor(status: string): string {
  switch (status) {
    case 'success':
    case 'succeeded':
    case 'completed':
      return 'green'
    case 'failed':
    case 'error':
      return 'red'
    case 'running':
    case 'started':
      return 'blue'
    default:
      return 'default'
  }
}

// shortHash returns the first characters of a hash so table rows stay narrow.
export function shortHash(hash: string, length = 12): string {
  if (hash.length <= length) return hash
  return `${hash.slice(0, length)}…`
}
