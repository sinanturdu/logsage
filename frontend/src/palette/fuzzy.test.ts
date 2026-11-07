import { describe, expect, it } from 'vitest'
import { fuzzyMatch, rank } from './fuzzy'

const items = (...labels: string[]) => labels.map((label) => ({ label, keywords: [] as string[] }))

describe('fuzzyMatch', () => {
  it('matches case-insensitive subsequences and reports positions', () => {
    expect(fuzzyMatch('gtl', 'Go to Logs')?.positions).toEqual([0, 3, 6])
    expect(fuzzyMatch('LOGS', 'Go to Logs')?.positions).toEqual([6, 7, 8, 9])
    expect(fuzzyMatch('xyz', 'Go to Logs')).toBeNull()
    expect(fuzzyMatch('logsx', 'Logs')).toBeNull()
  })

  it('treats an empty or blank query as a match with no highlight, and ignores spaces', () => {
    expect(fuzzyMatch('', 'Anything')).toEqual({ score: 0, positions: [] })
    expect(fuzzyMatch('   ', 'Anything')).toEqual({ score: 0, positions: [] })
    expect(fuzzyMatch('go logs', 'Go to Logs')).not.toBeNull()
  })

  it('prefers prefixes, word starts and consecutive runs', () => {
    const s = (q: string, t: string) => fuzzyMatch(q, t)?.score ?? -1
    expect(s('ref', 'Refresh')).toBeGreaterThan(s('ref', 'Show errors and fatal only'))
    expect(s('zo', 'Zoom out')).toBeGreaterThan(s('zo', 'Last 1 hour zone'))
    expect(s('live', 'Turn live tail on')).toBeGreaterThan(s('live', 'Clear filters via everything'))
  })

  it('handles special characters literally', () => {
    expect(fuzzyMatch('...', 'Custom range...')?.positions).toEqual([12, 13, 14])
    expect(fuzzyMatch('(', 'a (b)')?.positions).toEqual([2])
    expect(fuzzyMatch('.*', 'Logs')).toBeNull() // not a regular expression
    expect(fuzzyMatch('ü', 'Über')?.positions).toEqual([0])
    expect(fuzzyMatch('<img', 'Switch project: <img src=x>')?.positions).toEqual([16, 17, 18, 19])
  })
})

describe('rank', () => {
  it('orders best first and keeps command order for ties', () => {
    const r = rank('s', items('Last 6 hours', 'Show all levels', 'Sign out', 'Refresh'))
    expect(r.map((x) => x.item.label)).toEqual(['Show all levels', 'Sign out', 'Last 6 hours', 'Refresh'])
    expect(rank('', items('b', 'a')).map((x) => x.item.label)).toEqual(['b', 'a'])
  })

  it('falls back to keywords below any label match, without highlights', () => {
    const r = rank('logout', [
      { label: 'Sign out', keywords: ['logout'] },
      { label: 'Go to Logs', keywords: [] },
      { label: 'Log out of nothing', keywords: [] },
    ])
    expect(r.map((x) => x.item.label)).toEqual(['Log out of nothing', 'Sign out'])
    expect(r[1]?.positions).toEqual([])
  })

  it('is deterministic', () => {
    const list = items('Go to Logs', 'Go to API keys', 'Switch project: demo', 'Show all levels')
    expect(rank('go', list)).toEqual(rank('go', [...list]))
  })
})
