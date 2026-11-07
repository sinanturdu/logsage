// A small deterministic fuzzy matcher for the command palette (ADR 0020).
// Case-insensitive subsequence matching, scored so that prefixes, word
// starts and runs of consecutive characters rank first.

export type Match = { score: number; positions: number[] }

const PREFIX = 10
const WORD_START = 8
const CONSECUTIVE = 6
const BASE = 1
const GAP = 0.05

const SEPARATORS = new Set([' ', '-', '_', ':', '.', '/', '(', ')', '[', ']', ','])

const fold = (c: string) => c.toLocaleLowerCase('en-US')

function bonus(chars: readonly string[], j: number): number {
  if (j === 0) return PREFIX
  return SEPARATORS.has(chars[j - 1] ?? '') ? WORD_START : 0
}

/**
 * The best match of query in text, or null when the query is not a
 * subsequence. Whitespace in the query is ignored, so "go logs" matches
 * "Go to Logs". Positions index Array.from(text), for highlighting.
 */
export function fuzzyMatch(query: string, text: string): Match | null {
  const q = Array.from(query).filter((c) => c.trim() !== '').map(fold)
  if (q.length === 0) return { score: 0, positions: [] }
  const t = Array.from(text)
  const lower = t.map(fold)
  const n = q.length
  const m = t.length
  if (n > m) return null
  // best[i*m + j]: the best score with q[i] matched at t[j]; from[i*m + j]: its previous position.
  const best = new Array<number>(n * m).fill(-Infinity)
  const from = new Array<number>(n * m).fill(-1)
  const at = (a: number[], i: number, j: number, none: number) => a[i * m + j] ?? none
  for (let j = 0; j < m; j++) {
    if (lower[j] === q[0]) best[j] = BASE + bonus(t, j) - j * GAP
  }
  for (let i = 1; i < n; i++) {
    for (let j = i; j < m; j++) {
      if (lower[j] !== q[i]) continue
      let top = -Infinity
      let prevAt = -1
      for (let k = i - 1; k < j; k++) {
        const prev = at(best, i - 1, k, -Infinity)
        if (prev === -Infinity) continue
        const s = prev + BASE + bonus(t, j) + (k === j - 1 ? CONSECUTIVE : -(j - k - 1) * GAP)
        if (s > top) {
          top = s
          prevAt = k
        }
      }
      best[i * m + j] = top
      from[i * m + j] = prevAt
    }
  }
  let end = -1
  let score = -Infinity
  for (let j = 0; j < m; j++) {
    const s = at(best, n - 1, j, -Infinity)
    if (s > score) {
      score = s
      end = j
    }
  }
  if (end === -1) return null
  const positions: number[] = []
  for (let i = n - 1, j = end; i >= 0 && j >= 0; i--) {
    positions.unshift(j)
    j = at(from, i, j, -1)
  }
  return { score, positions }
}

export type Rankable = { label: string; keywords: readonly string[] }
export type Ranked<T> = { item: T; score: number; positions: number[] }

/**
 * The items that match, best first. A label match beats a keyword match;
 * equal scores keep the items' original order.
 */
export function rank<T extends Rankable>(query: string, items: readonly T[]): Ranked<T>[] {
  const out: (Ranked<T> & { index: number })[] = []
  items.forEach((item, index) => {
    const label = fuzzyMatch(query, item.label)
    if (label) {
      out.push({ item, score: label.score + 100, positions: label.positions, index })
      return
    }
    let kw = -Infinity
    for (const k of item.keywords) {
      const m = fuzzyMatch(query, k)
      if (m && m.score > kw) kw = m.score
    }
    if (kw > -Infinity) out.push({ item, score: kw, positions: [], index })
  })
  out.sort((a, b) => b.score - a.score || a.index - b.index)
  return out.map(({ item, score, positions }) => ({ item, score, positions }))
}
