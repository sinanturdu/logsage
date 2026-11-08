import { describe, expect, it } from 'vitest'
import { combineGroups, sumSparklines, type Row } from './combine'
import { group } from './testing'

const DISK = 'failed to write /var/data/exports/<n>.json: disk quota exceeded'
const fp = (c: string) => c.repeat(16)

const titles = (rows: Row[]) => rows.map((r) => (r.kind === 'group' ? `${r.group.fingerprint}` : `parent:${r.groups.map((g) => g.fingerprint[0]).join('')}`))

describe('combineGroups', () => {
  const auth = group(fp('a'), { title: DISK, services: ['auth'], count: 54, levels: ['error'], first_seen: '2026-11-07T19:00:00.000Z', last_seen: '2026-11-07T20:10:00.000Z', sparkline: [1, 2, 3], status: 'open' })
  const inv = group(fp('b'), { title: DISK, services: ['inventory'], count: 46, levels: ['warn', 'error'], first_seen: '2026-11-07T18:00:00.000Z', last_seen: '2026-11-07T20:12:00.000Z', sparkline: [0, 5, 1], status: 'resolved', regressed: true })
  const other = group(fp('c'), { title: 'connection to db-<n>.internal:<n> refused', services: ['auth'], count: 80 })
  const check = group(fp('d'), { title: DISK, services: ['checkout'], count: 39, levels: ['fatal'], is_new: true, sparkline: [2, 0, 0] })

  it('collapses the same template across services and sums, merges and flags', () => {
    const rows = combineGroups([auth, inv, other, check], 'count')
    expect(titles(rows)).toEqual(['parent:abd', fp('c')])
    const p = rows[0]
    if (p?.kind !== 'parent') throw new Error('parent expected')
    expect(p.key).toBe(DISK)
    expect(p.combined).toEqual({
      title: DISK,
      count: 139,
      services: ['auth', 'inventory', 'checkout'],
      levels: ['warn', 'error', 'fatal'],
      first_seen: '2026-11-07T18:00:00.000Z',
      last_seen: '2026-11-07T20:12:00.000Z',
      status: 'mixed',
      is_new: true,
      regressed: true,
      sparkline: [3, 7, 4],
    })
    expect(p.groups.map((g) => g.fingerprint)).toEqual([fp('a'), fp('b'), fp('d')]) // the API's order
  })

  it('keeps a shared status', () => {
    const rows = combineGroups([auth, { ...inv, status: 'open', regressed: false }], 'count')
    expect(rows[0]?.kind === 'parent' && rows[0].combined.status).toBe('open')
    expect(rows[0]?.kind === 'parent' && rows[0].combined.regressed).toBe(false)
  })

  it('leaves single groups as they are', () => {
    expect(combineGroups([other], 'count')).toEqual([{ kind: 'group', group: other }])
    expect(combineGroups([], 'count')).toEqual([])
  })

  it('sorts by the combined values, ties in the API order', () => {
    // By count: 139 (combined) before 80, although each disk group alone is smaller.
    expect(titles(combineGroups([other, auth, inv, check], 'count'))).toEqual(['parent:abd', fp('c')])
    // By last seen: the combined last seen is the latest child's.
    const late = { ...other, last_seen: '2026-11-07T20:11:00.000Z' }
    expect(titles(combineGroups([late, auth, inv], 'last_seen'))).toEqual(['parent:ab', fp('c')])
    // By first seen: the earliest child decides, so an old parent goes after a newer single group.
    const fresh = { ...other, first_seen: '2026-11-07T20:00:00.000Z' }
    expect(titles(combineGroups([auth, fresh, inv], 'first_seen'))).toEqual([fp('c'), 'parent:ab'])
    // Equal values keep the order of first appearance.
    const x = group(fp('e'), { title: 'x', count: 5 })
    const y = group(fp('f'), { title: 'y', count: 5 })
    expect(titles(combineGroups([x, y], 'count'))).toEqual([fp('e'), fp('f')])
    expect(titles(combineGroups([y, x], 'count'))).toEqual([fp('f'), fp('e')])
  })

  it('treats equal templates in one service as one parent too, and different templates apart', () => {
    const twin = group(fp('g'), { title: DISK, services: ['auth'], count: 1, error_type: 'OSError' })
    const rows = combineGroups([auth, twin], 'count')
    expect(rows).toHaveLength(1)
    expect(rows[0]?.kind === 'parent' && rows[0].combined.services).toEqual(['auth'])
  })
})

describe('sumSparklines', () => {
  it('adds bucket by bucket', () => {
    expect(sumSparklines([[1, 2], [3, 4, 5], []])).toEqual([4, 6, 5])
    expect(sumSparklines([])).toEqual([])
  })
})
