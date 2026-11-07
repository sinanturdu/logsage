/**
 * Copies text: the Clipboard API when the page may use it, otherwise a
 * hidden textarea and the legacy copy command. Returns whether it worked.
 */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // Permission denied or insecure context: try the fallback.
  }
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', '')
  area.style.position = 'fixed'
  area.style.opacity = '0'
  document.body.appendChild(area)
  area.select()
  let ok: boolean
  try {
    ok = typeof document.execCommand === 'function' && document.execCommand('copy')
  } catch {
    ok = false
  }
  area.remove()
  return ok
}
