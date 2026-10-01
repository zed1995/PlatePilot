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
        padding: 12,
        maxHeight: 400,
        overflow: 'auto',
        background: '#fafafa',
        border: '1px solid #f0f0f0',
        borderRadius: 6,
        fontSize: 12,
      }}
    >
      {text}
    </pre>
  )
}
