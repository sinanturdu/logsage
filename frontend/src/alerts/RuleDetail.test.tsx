import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { commandsFor } from '../palette/commands'
import { json, renderApp } from '../test/util'
import { NAV_POLL_MS } from './api'
import { channel, delivery, event, firingState, mockAlerts, rule } from './testing'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
})

const B = '/api/projects/shop/alerts'
const page = <T,>(key: string, items: T[], next: number | null = null) => json(200, { [key]: items, next_before: next })

describe('rule detail', () => {
  it('shows the summary, the state per group, the timeline and the deliveries with Load more', async () => {
    const r = rule('r1', { name: 'New groups', type: 'new_group', params: { min_events: 1 }, window: '15m0s', state: 'firing', firing: 2, note: 'Check the deploy.' })
    const { requests } = mockAlerts({
      [`GET ${B}/rules/r1`]: () => json(200, { rule: r }),
      [`GET ${B}/states`]: () =>
        json(200, {
          states: [
            firingState('r1', { dedup_key: 'fp:1', fingerprint: 'aaaaaaaaaaaaaaaa', template: 'db timeout after <n> ms', value: 12 }),
            firingState('r1', { dedup_key: 'fp:2', fingerprint: 'bbbbbbbbbbbbbbbb', template: 'cache miss <id>', value: 3 }),
            firingState('other'),
          ],
        }),
      [`GET ${B}/events`]: (q) =>
        q.params.get('before')
          ? page('events', [event(1, { from: 'ok', to: 'pending', value: 1, threshold: 1, rule_type: 'new_group' })])
          : page('events', [event(3, { to: 'firing', value: 12, threshold: 1, rule_type: 'new_group', template: 'db timeout after <n> ms', fingerprint: 'aaaaaaaaaaaaaaaa' })], 3),
      [`GET ${B}/notifications`]: (q) =>
        q.params.get('before')
          ? page('notifications', [delivery(1, { status: 'sent' })])
          : page('notifications', [delivery(2, { status: 'failed', attempts: 8, response_status: 500, last_error: 'post https://hooks.example.com: status 500' })], 2),
    })
    renderApp('/projects/shop/alerts/rules/r1')
    expect((await screen.findByRole('heading', { level: 1 })).textContent).toBe('New groups')
    expect(screen.getByRole('link', { name: 'All alert rules' }).getAttribute('href')).toBe('/projects/shop/alerts')
    expect(screen.getByText('Check the deploy.')).toBeTruthy()
    expect(document.querySelector('.facts')?.textContent).toContain('New error group with ≥ 1 event over 15 minutes')

    const states = await screen.findByRole('table', { name: 'Pending and firing groups of this rule' })
    const rows = within(states).getAllByRole('row').slice(1)
    expect(rows.length).toBe(2)
    const link = within(rows[0] as HTMLElement).getByRole('link')
    expect(link.getAttribute('href')).toBe('/projects/shop/groups/aaaaaaaaaaaaaaaa')
    expect(link.textContent).toBe('db timeout after <n> ms')
    expect(rows[0]?.textContent).toContain('◉ firing')
    expect(rows[0]?.textContent).toContain('12 (threshold 1)')

    expect(requests.find((q) => q.path === `${B}/events`)?.params.get('rule')).toBe('r1')
    expect(requests.find((q) => q.path === `${B}/notifications`)?.params.get('rule')).toBe('r1')
    const timeline = document.querySelector('.timeline') as HTMLElement
    expect(timeline.textContent).toContain('2026-11-09 12:00:00 UTC: pending → ◉ firing, value 12 (threshold 1), db timeout after <n> ms')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Load more events' }))
    await waitFor(() => expect(timeline.querySelectorAll('li').length).toBe(2))

    const deliveries = screen.getByRole('table', { name: 'Notifications of this rule, newest first' })
    const head = within(deliveries).getAllByRole('columnheader').map((h) => h.textContent)
    expect(head).toEqual(['Created (UTC)', 'Channel', 'Event', 'Status', 'Attempts', 'Last HTTP status', 'Next attempt (UTC)'])
    expect(within(deliveries).getAllByRole('row')[1]?.textContent).toContain('✕ failed: post https://hooks.example.com: status 500')
    await user.click(screen.getByRole('button', { name: 'Load more deliveries' }))
    await waitFor(() => expect(within(deliveries).getAllByRole('row').length).toBe(3))
    expect(screen.queryByRole('button', { name: 'Load more deliveries' })).toBeNull()
  })

  it('enables, disables (asking first while firing) and deletes for owners; viewers only read', async () => {
    const r = rule('r1', { name: 'Errors', state: 'firing', firing: 1 })
    const { requests } = mockAlerts({
      [`GET ${B}/rules/r1`]: () => json(200, { rule: r }),
      [`POST ${B}/rules/r1/disable`]: () => json(200, { rule: { ...r, enabled: false } }),
      [`DELETE ${B}/rules/r1`]: () => json(204),
    })
    renderApp('/projects/shop/alerts/rules/r1')
    await screen.findByRole('heading', { level: 1 })
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Disable' }))
    const dialog = await screen.findByRole('dialog', { name: 'Disable this firing rule?' })
    await user.click(within(dialog).getByRole('button', { name: 'Disable rule' }))
    await waitFor(() => expect(requests.some((q) => q.path === `${B}/rules/r1/disable`)).toBe(true))
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    const del = await screen.findByRole('dialog', { name: 'Delete this alert rule?' })
    await user.click(within(del).getByRole('button', { name: 'Delete rule' }))
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/projects/shop/alerts'))
  })

  it('is read-only for viewers and says when a rule does not exist', async () => {
    mockAlerts({ [`GET ${B}/rules/r1`]: () => json(200, { rule: rule('r1') }) }, 'viewer')
    renderApp('/projects/shop/alerts/rules/r1')
    await screen.findByRole('heading', { level: 1 })
    expect(screen.queryByRole('button', { name: /Disable|Enable|Edit|Delete/ })).toBeNull()
    expect(await screen.findByText('Nothing is pending or firing.')).toBeTruthy()
  })

  it('says when a rule does not exist', async () => {
    mockAlerts({})
    renderApp('/projects/shop/alerts/rules/nope')
    expect((await screen.findByRole('alert')).textContent).toContain('This alert rule does not exist in this project.')
  })
})

describe('activity', () => {
  it('lists events with a rule filter in the URL, Load more, and failed deliveries in words', async () => {
    const { requests } = mockAlerts({
      [`GET ${B}/rules`]: () => json(200, { rules: [rule('r1', { name: 'Errors' }), rule('r2', { name: 'Volume' })] }),
      [`GET ${B}/events`]: (q) => (q.params.get('before') ? page('events', [event(1, { from: 'firing', to: 'resolved', reason: 'rule_disabled' })]) : page('events', [event(2)], 2)),
      [`GET ${B}/notifications`]: (q) =>
        q.params.get('status') === 'failed' ? page('notifications', [delivery(4, { status: 'failed', attempts: 8, response_status: 503, rule_name: 'Errors' })]) : page('notifications', []),
    })
    renderApp('/projects/shop/alerts?tab=activity')
    const table = await screen.findByRole('table', { name: 'State changes of the alert rules, newest first' })
    expect(within(table).getAllByRole('row')[1]?.textContent).toContain('2026-11-09 12:00:00 UTC')
    expect(within(table).getAllByRole('row')[1]?.textContent).toContain('56.52% (threshold 20%)')
    const failed = screen.getByRole('region', { name: /Failed deliveries/ })
    expect(failed.textContent).toContain('✕ failed Errors to ops c1, alert.firing, after 8 attempts, HTTP 503')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(within(table).getAllByRole('row').length).toBe(3))
    expect(within(table).getAllByRole('row')[2]?.textContent).toContain('(rule disabled)')
    await user.selectOptions(screen.getByLabelText('Rule'), 'r2')
    expect(screen.getByTestId('location').textContent).toBe('/projects/shop/alerts?tab=activity&rule=r2')
    await waitFor(() => expect(requests.some((q) => q.path === `${B}/events` && q.params.get('rule') === 'r2')).toBe(true))
  })

  it('shows the empty state', async () => {
    mockAlerts({})
    renderApp('/projects/shop/alerts?tab=activity')
    expect(await screen.findByText('No alert activity yet. State changes of your rules appear here.')).toBeTruthy()
  })
})

describe('nav firing count', () => {
  it('says "Alerts, 2 firing", polls every 30 s while visible, pauses while hidden and respects 429', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    let states = [firingState('a'), firingState('a', { dedup_key: 'fp:1' }), firingState('b'), { ...firingState('c'), state: 'pending' as const }]
    let limited = false
    const { requests } = mockAlerts({
      [`GET ${B}/states`]: () => (limited ? json(429, { error: 'slow' }, { 'Retry-After': '90' }) : json(200, { states })),
    })
    renderApp('/projects/shop/logs')
    expect(await screen.findByRole('link', { name: 'Alerts, 2 firing' })).toBeTruthy()
    const polls = () => requests.filter((q) => q.path === `${B}/states`).length
    expect(polls()).toBe(1)
    states = [firingState('a')]
    await act(() => vi.advanceTimersByTimeAsync(NAV_POLL_MS))
    expect(await screen.findByRole('link', { name: 'Alerts, 1 firing' })).toBeTruthy()
    expect(polls()).toBe(2)

    Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true })
    document.dispatchEvent(new Event('visibilitychange', { bubbles: true })) // as browsers do: it reaches window
    await act(() => vi.advanceTimersByTimeAsync(NAV_POLL_MS * 4))
    expect(polls()).toBe(2)

    states = []
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
    document.dispatchEvent(new Event('visibilitychange', { bubbles: true })) // as browsers do: it reaches window
    expect(await screen.findByRole('link', { name: 'Alerts' })).toBeTruthy()
    expect(polls()).toBe(3)

    limited = true
    await act(() => vi.advanceTimersByTimeAsync(NAV_POLL_MS))
    expect(polls()).toBe(4)
    await act(() => vi.advanceTimersByTimeAsync(NAV_POLL_MS * 2))
    expect(polls()).toBe(4) // waits the 90 s of Retry-After
    await act(() => vi.advanceTimersByTimeAsync(NAV_POLL_MS))
    expect(polls()).toBe(5)
  })

  it('stays "Alerts" and stops polling when alerts are off', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const { requests } = mockAlerts({ [`GET ${B}/states`]: () => json(503, { error: 'alerts are disabled: set ALERTS_ENABLED=true and ALERTS_SECRET_KEY' }) })
    renderApp('/projects/shop/logs')
    await waitFor(() => expect(requests.filter((q) => q.path === `${B}/states`).length).toBe(1))
    await act(() => vi.advanceTimersByTimeAsync(NAV_POLL_MS * 3))
    expect(requests.filter((q) => q.path === `${B}/states`).length).toBe(1)
    expect(screen.getByRole('link', { name: 'Alerts' })).toBeTruthy()
  })
})

describe('palette commands for alerts', () => {
  it('adds Go to Alerts and, for owners and admins, the create commands', () => {
    const base = { slug: 'shop', section: 'logs' as const, projects: ['shop'], explorer: null }
    const labels = (c: Parameters<typeof commandsFor>[0]) => commandsFor(c).filter((x) => x.group === 'Navigation').map((x) => [x.label, x.action])
    expect(labels({ ...base, canManageAlerts: true })).toEqual(
      expect.arrayContaining([
        ['Go to Alerts', { type: 'navigate', to: '/projects/shop/alerts' }],
        ['Create alert rule...', { type: 'navigate', to: '/projects/shop/alerts?create=rule' }],
        ['Create channel...', { type: 'navigate', to: '/projects/shop/alerts?tab=channels&create=channel' }],
      ]),
    )
    expect(labels({ ...base, canManageAlerts: false }).map((l) => l[0])).not.toContain('Create alert rule...')
    expect(labels({ ...base, section: 'alerts' }).map((l) => l[0])).not.toContain('Go to Alerts')
  })

  it('runs Create alert rule from the palette', async () => {
    mockAlerts({ [`GET ${B}/channels`]: () => json(200, { channels: [channel('c1')] }) })
    renderApp('/projects/shop/logs')
    await screen.findByRole('link', { name: 'Alerts' })
    const user = userEvent.setup()
    await user.keyboard('{Control>}k{/Control}')
    await user.keyboard('Create alert rule')
    await user.keyboard('{Enter}')
    expect(await screen.findByRole('dialog', { name: 'Create an alert rule' })).toBeTruthy()
  })
})
