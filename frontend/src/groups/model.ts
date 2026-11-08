// The error groups' URL model (ADR 0023). Like the explorer's (ADR 0017),
// everything a user chooses lives in the URL; parsing drops invalid values
// instead of throwing.

const FINGERPRINT = /^[0-9a-f]{16}$/

/**
 * A group fingerprint from a URL: 16 hexadecimal characters (upper case is
 * accepted and lowered). All zeros means "not grouped", so it is no group.
 */
export function parseFingerprint(v: string | null | undefined): string | null {
  const s = (v ?? '').toLowerCase()
  return FINGERPRINT.test(s) && s !== '0000000000000000' ? s : null
}
