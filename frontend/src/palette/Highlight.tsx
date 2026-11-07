/** Text with matched characters in plain spans; never HTML. */
export function Highlight({ text, positions }: { text: string; positions: readonly number[] }) {
  if (positions.length === 0) return <>{text}</>
  const hit = new Set(positions)
  const parts: { s: string; on: boolean }[] = []
  Array.from(text).forEach((c, i) => {
    const on = hit.has(i)
    const last = parts[parts.length - 1]
    if (last && last.on === on) last.s += c
    else parts.push({ s: c, on })
  })
  return (
    <>
      {parts.map((p, i) =>
        p.on ? (
          <span key={i} className="hit">
            {p.s}
          </span>
        ) : (
          <span key={i}>{p.s}</span>
        ),
      )}
    </>
  )
}
