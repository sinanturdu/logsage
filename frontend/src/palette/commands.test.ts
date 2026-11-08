import { describe, expect, it } from 'vitest'
import { commandsFor, defaultCommands, type AppContext, type ExplorerContext } from './commands'

const EX: ExplorerContext = { preset: '15m', live: false, zoom: false, canZoom: false, services: ['api', 'checkout'], selectedServices: [], levels: [], hasFilters: false }
const ctx = (over: Partial<AppContext> = {}): AppContext => ({ slug: 'demo', section: 'logs', projects: ['demo', 'shop'], explorer: EX, ...over })
const labels = (c: AppContext) => commandsFor(c).map((x) => x.label)

describe('commandsFor', () => {
  it('lists explorer commands on the logs page', () => {
    expect(labels(ctx())).toEqual([
      'Go to Error groups',
      'Go to API keys',
      'Switch project: shop',
      'Last 15 minutes',
      'Last 1 hour',
      'Last 6 hours',
      'Last 24 hours',
      'Last 7 days',
      'Custom range...',
      'Show errors and fatal only',
      'Filter by service: api',
      'Filter by service: checkout',
      'Focus search',
      'Focus trace id',
      'Turn live tail on',
      'Refresh',
      'Copy link to this view',
      'Change password...',
      'Sign out of all devices...',
      'Sign out',
      'Show keyboard shortcuts',
    ])
  })

  it('offers only what applies', () => {
    const l = labels(ctx({ explorer: { ...EX, live: true, zoom: true, levels: ['error', 'fatal'], hasFilters: true, selectedServices: ['api'] } }))
    expect(l).toContain('Turn live tail off')
    expect(l).toContain('Zoom out')
    expect(l).not.toContain('Zoom to selection')
    expect(l).toContain('Show all levels')
    expect(l).not.toContain('Show errors and fatal only')
    expect(l).toContain('Clear filters')
    expect(l).not.toContain('Filter by service: api')
    expect(labels(ctx({ explorer: { ...EX, canZoom: true } }))).toContain('Zoom to selection')
  })

  it('on the keys page: no explorer commands, Go to Logs, project switch keeps the section', () => {
    const c = commandsFor(ctx({ section: 'keys', explorer: null }))
    expect(c.map((x) => x.label)).toEqual([
      'Go to Logs',
      'Go to Error groups',
      'Switch project: shop',
      'Copy link to this view',
      'Change password...',
      'Sign out of all devices...',
      'Sign out',
      'Show keyboard shortcuts',
    ])
    expect(c.find((x) => x.id === 'project:shop')?.action).toEqual({ type: 'navigate', to: '/projects/shop/keys' })
  })

  it('without a project: account and help only', () => {
    expect(labels({ slug: null, section: 'other', projects: [], explorer: null })).toEqual([
      'Copy link to this view',
      'Change password...',
      'Sign out of all devices...',
      'Sign out',
      'Show keyboard shortcuts',
    ])
  })

  it('encodes project slugs in paths', () => {
    const c = commandsFor(ctx({ projects: ['demo', 'a/b'] }))
    expect(c.find((x) => x.id === 'project:a/b')?.action).toEqual({ type: 'navigate', to: '/projects/a%2Fb/logs' })
  })
})

describe('defaultCommands', () => {
  const labels = (c: AppContext, recent: string[] = []) => defaultCommands(commandsFor(c), recent, c.explorer).map((x) => x.label)

  it('on the logs page: the other page, live, the next ranges, projects, shortcuts', () => {
    expect(labels(ctx())).toEqual(['Go to Error groups', 'Go to API keys', 'Turn live tail on', 'Last 1 hour', 'Last 6 hours', 'Switch project: shop', 'Show keyboard shortcuts'])
  })

  it('adds Clear filters only with filters, and says Turn live tail off while live', () => {
    const l = labels(ctx({ explorer: { ...EX, hasFilters: true, live: true, preset: '24h' } }))
    expect(l).toEqual(['Go to Error groups', 'Go to API keys', 'Turn live tail off', 'Clear filters', 'Last 7 days', 'Last 6 hours', 'Switch project: shop', 'Show keyboard shortcuts'])
  })

  it('picks ranges for an absolute selection', () => {
    expect(labels(ctx({ explorer: { ...EX, preset: null } }))).toContain('Last 15 minutes')
  })

  it('on the keys page and with one project: no explorer commands, no switch', () => {
    expect(labels(ctx({ section: 'keys', explorer: null, projects: ['demo'] }))).toEqual(['Go to Logs', 'Go to Error groups', 'Show keyboard shortcuts'])
  })

  it('puts up to two recent commands first, at most 8 in all, and always keeps the shortcuts help', () => {
    const many = ctx({ projects: ['demo', 'a', 'b', 'c', 'd', 'e'], explorer: { ...EX, hasFilters: true } })
    const l = labels(many, ['refresh', 'zoom:out', 'range:7d', 'focus:search'])
    expect(l[0]).toBe('Refresh') // zoom:out does not apply, so it is skipped
    expect(l[1]).toBe('Last 7 days')
    expect(l).not.toContain('Focus search') // only two recent
    expect(l).toHaveLength(8)
    expect(l.at(-1)).toBe('Show keyboard shortcuts')
  })
})

describe('error groups commands', () => {
  const gctx = (over: Partial<AppContext> = {}): AppContext =>
    ctx({ section: 'groups', explorer: null, groups: { status: 'open', sort: 'count', newOnly: false }, ...over })

  it('offers the other statuses, new only and the other sorts', () => {
    const c = commandsFor(gctx())
    const l = c.map((x) => x.label)
    expect(l).toEqual(expect.arrayContaining(['Go to Logs', 'Go to API keys', 'Show ignored groups', 'Show resolved groups', 'Show groups of every status', 'Show new groups only', 'Sort groups by last seen', 'Sort groups by first seen']))
    expect(l).not.toContain('Go to Error groups')
    expect(l).not.toContain('Show open groups')
    expect(l).not.toContain('Sort groups by most events')
    expect(c.find((x) => x.id === 'group-status:resolved')?.action).toEqual({ type: 'group-status', status: 'resolved' })
    expect(c.find((x) => x.id === 'group-new')?.action).toEqual({ type: 'group-new', on: true })
    expect(c.find((x) => x.id === 'project:shop')?.action).toEqual({ type: 'navigate', to: '/projects/shop/groups' })
  })

  it('with new only on, offers to turn it off and hides the sorts (new only sorts by first seen)', () => {
    const l = labels(gctx({ groups: { status: 'all', sort: 'count', newOnly: true } }))
    expect(l).toContain('Show groups that are not new too')
    expect(l).toContain('Show open groups')
    expect(l.some((x) => x.startsWith('Sort groups'))).toBe(false)
  })

  it('other pages have no group commands but can go to Error groups', () => {
    const l = labels(ctx())
    expect(l).toContain('Go to Error groups')
    expect(l.some((x) => /groups (of|by)|new groups/.test(x))).toBe(false)
  })
})
