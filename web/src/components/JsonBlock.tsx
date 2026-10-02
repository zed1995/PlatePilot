// JsonBlock renders an already-decoded JSON value as readable, indented text
// in a scrollable box. It performs no editing.
export default function JsonBlock({ value }: { value: unknown }) {
  const text =
    value === null || value === undefined
      ? ''
      : typeof value === 'string'
        ? value
        : JSON.stringify(value, null, 2)

  return (
    <pre
      style={{
        margin: 0,
        padding: 14,
        maxHeight: 400,
        overflow: 'auto',
        background: '#f5f5f7',
        border: '1px solid rgba(0, 0, 0, 0.04)',
        borderRadius: 10,
        fontSize: 12,
        fontFamily:
          'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      }}
    >
      {text}
    </pre>
  )
}
