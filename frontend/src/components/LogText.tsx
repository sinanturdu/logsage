// LogText renders untrusted text (log messages, attribute values, anything
// that came from a client of the ingest API) strictly as text. React escapes
// string children; this component exists so every place that shows log data
// goes through one reviewed path and never through HTML (ADR 0016).
type Props = { text: string; mono?: boolean; className?: string }

export function LogText({ text, mono = true, className }: Props) {
  const cls = [mono ? 'mono' : '', className ?? ''].filter(Boolean).join(' ')
  return <span className={cls || undefined}>{text}</span>
}
