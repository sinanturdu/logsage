import { describe, expect, it } from 'vitest'
import { commandsFor, defaultCommands, type AppContext, type ExplorerContext } from './commands'

const EX: ExplorerContext = { live: false, zoom: false, canZoom: false, services: ['api', 'checkout'], selectedServices: [], levels: [], hasFilters: false }
const ctx = (over: Partial<AppContext> = {}): AppContext => ({ slug: 'demo', section: 'logs', projects: ['demo', 'shop'], explorer: EX, ...over })
const labels = (c: AppContext) => commandsFor(c).map((x) => x.label)

describe('commandsFor', () => {
  it('lists explorer commands on the logs page', () => {
    expect(labels(ctx())).toEqual([
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
  it('shows recent commands that still apply, then navigation', () => {
    const all = commandsFor(ctx())
    expect(defaultCommands(all, ['refresh', 'zoom:out', 'go-keys']).map((c) => c.label)).toEqual(['Refresh', 'Go to API keys', 'Switch project: shop'])
    expect(defaultCommands(all, []).map((c) => c.label)).toEqual(['Go to API keys', 'Switch project: shop'])
  })
})
