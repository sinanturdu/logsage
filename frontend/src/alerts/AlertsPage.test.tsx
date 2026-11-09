import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { json, renderApp } from '../test/util'
import { DELETE_FIRING, DISABLE_FIRING, EMPTY_RULES, VIEWER_ALERTS } from './AlertsPage'
import { KEY_COMMAND } from './parts'
import { OFF, XSS, channel, event, mockAlerts, rule } from './testing'

afterEach(() => {
  vi.unstubAllGlobals()
  delete (window as { __xss?: number }).__xss
})

const B = '/api/projects/shop/alerts'
const RULES = [
  rule('r1', { name: 'High error rate', state: 'firing', firing: 1, last_change_at: '2026-11-09T12:00:00Z' }),
  rule('r2', { name: 'Silence', type: 'log_volume', params: { op: 'below', count: 1 }, window: '10m0s', state: 'pending', pending: 1 }),
  rule('r3', { name: 'New groups', type: 'new_group', params: { min_events: 1 }, state: 'resolved' }),
  rule('r4', { name: 'Off rule', enabled: false, state: 'ok', channels: ['c1', 'c2'] }),
  rule('r5', { name: 'Quiet', state: 'ok' }),
]
const CHANNELS = [channel('c1', { name: 'Slack ops', kind: 'slack', url_host: 'https://hooks.slack.com' }), channel('c2', { name: 'pager' })]
const rows = async () => within(await screen.findByRole('table', { name: 'Alert rules of this project' })).getAllByRole('row').slice(1)

describe('alerts page: rules', () => {
  it('lists rules with type, condition, state by shape and word, last change and channels', async () => {
    mockAlerts({ [`GET ${B}/rules`]: () => json(200, { rules: RULES }), [`GET ${B}/channels`]: () => json(200, { channels: CHANNELS }) })
    renderApp('/projects/shop/alerts')
    const r = await rows()
    expect(r.map((row) => within(row).getAllByRole('cell')[3]?.textContent)).toEqual(['◉ firing', '◐ pending', '✓ resolved', '⊘ disabled', '○ ok'])
    expect(r[0]?.textContent).toContain('Error rate')
    expect(r[0]?.textContent).toContain('Errors ≥ 20% of logs over 5 minutes, at least 50 logs')
    expect(r[1]?.textContent).toContain('No logs over 10 minutes')
    expect(r[3]?.textContent).toContain('2: Slack ops, pager')
    const time = r[0]?.querySelector('time')
    expect(time?.getAttribute('title')).toBe('2026-11-09 12:00:00 UTC')
    expect(r[4]?.textContent).toContain('no changes yet')
    expect(within(r[0] as HTMLElement).getByRole('link', { name: 'High error rate' }).getAttribute('href')).toBe('/projects/shop/alerts/rules/r1')
    expect(screen.getByText('5 rules, 1 firing.')).toBeTruthy()
  })

  it('shows one clear state when alerts are turned off, and checks again', async () => {
    const { routes } = mockAlerts({ [`GET ${B}/rules`]: OFF, [`GET ${B}/channels`]: OFF })
    renderApp('/projects/shop/alerts')
    const panel = await screen.findByRole('alert')
    expect(within(panel).getByRole('heading').textContent).toBe('Alerts are turned off on this server.')
    expect(panel.textContent).toContain('ALERTS_ENABLED=true')
    expect(panel.textContent).toContain('ALERTS_SECRET_KEY')
    expect(panel.querySelector('pre')?.textContent).toBe(KEY_COMMAND)
    expect(screen.queryByRole('tablist')).toBeNull()
    routes[`GET ${B}/rules`] = () => json(200, { rules: [] })
    routes[`GET ${B}/channels`] = () => json(200, { channels: [] })
    await userEvent.setup().click(within(panel).getByRole('button', { name: 'Check again' }))
    expect(await screen.findByText(EMPTY_RULES)).toBeTruthy()
  })

  it('offers Try again for 5xx and network errors, not the off state', async () => {
    const { routes } = mockAlerts({ [`GET ${B}/rules`]: () => json(500) })
    renderApp('/projects/shop/alerts')
    const panel = await screen.findByRole('alert')
    expect(panel.textContent).toContain('The LogSage API failed (status 500)')
    routes[`GET ${B}/rules`] = () => json(200, { rules: RULES })
    await userEvent.setup().click(within(panel).getByRole('button', { name: 'Try again' }))
    expect((await rows()).length).toBe(5)
  })

  it('keeps the tab in the URL with real tab semantics', async () => {
    mockAlerts({})
    renderApp('/projects/shop/alerts')
    const tabs = await screen.findAllByRole('tab')
    expect(tabs.map((t) => t.textContent)).toEqual(['Rules', 'Channels', 'Activity'])
    expect(tabs[0]?.getAttribute('aria-selected')).toBe('true')
    const user = userEvent.setup()
    tabs[0]?.focus()
    await user.keyboard('{ArrowRight}')
    expect(screen.getByTestId('location').textContent).toBe('/projects/shop/alerts?tab=channels')
    expect(document.activeElement?.textContent).toBe('Channels')
    expect(screen.getByRole('tabpanel').getAttribute('aria-labelledby')).toBe(document.activeElement?.id)
    await user.keyboard('{End}')
    expect(screen.getByTestId('location').textContent).toBe('/projects/shop/alerts?tab=activity')
  })

  it('enables and disables for owners; a firing rule asks first and explains the final notice', async () => {
    const { requests } = mockAlerts({
      [`GET ${B}/rules`]: () => json(200, { rules: RULES }),
      [`POST ${B}/rules/r5/disable`]: () => json(200, { rule: rule('r5', { enabled: false }) }),
      [`POST ${B}/rules/r4/enable`]: () => json(200, { rule: rule('r4') }),
      [`POST ${B}/rules/r1/disable`]: () => json(200, { rule: rule('r1', { enabled: false }) }),
    })
    renderApp('/projects/shop/alerts')
    await rows()
    const user = userEvent.setup()
    const sw = screen.getByRole('switch', { name: 'Enabled: Quiet' })
    expect(sw.getAttribute('aria-checked')).toBe('true')
    await user.click(sw)
    await user.click(screen.getByRole('switch', { name: 'Enabled: Off rule' }))
    await waitFor(() => expect(requests.filter((r) => r.method === 'POST').map((r) => r.path)).toEqual([`${B}/rules/r5/disable`, `${B}/rules/r4/enable`]))
    await user.click(screen.getByRole('switch', { name: 'Enabled: High error rate' }))
    const dialog = await screen.findByRole('dialog', { name: 'Disable this firing rule?' })
    expect(dialog.textContent).toContain(DISABLE_FIRING)
    expect(document.activeElement?.textContent).toBe('Cancel')
    await user.click(within(dialog).getByRole('button', { name: 'Disable rule' }))
    await waitFor(() => expect(requests.some((r) => r.path === `${B}/rules/r1/disable`)).toBe(true))
  })

  it('is read-only for viewers', async () => {
    const { requests } = mockAlerts({ [`GET ${B}/rules`]: () => json(200, { rules: RULES }) }, 'viewer')
    renderApp('/projects/shop/alerts')
    const r = await rows()
    expect(screen.getByText(VIEWER_ALERTS)).toBeTruthy()
    expect(screen.queryByRole('switch')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Create rule' })).toBeNull()
    expect(screen.queryByRole('button', { name: /^Edit|^Delete/ })).toBeNull()
    expect(within(r[3] as HTMLElement).getAllByRole('cell')[5]?.textContent).toBe('Off')
    await userEvent.setup().click(screen.getByRole('tab', { name: 'Channels' }))
    await screen.findByRole('table', { name: 'Notification channels of this project' })
    expect(screen.queryByRole('button', { name: /Send a test|Rotate|Edit|Delete/ })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Create channel' })).toBeNull()
    expect(requests.every((x) => x.method === 'GET')).toBe(true)
  })

  it('deletes after a confirmation that explains the final resolved notice of a firing rule', async () => {
    const { requests } = mockAlerts({ [`GET ${B}/rules`]: () => json(200, { rules: RULES }), [`DELETE ${B}/rules/r1`]: () => json(204) })
    renderApp('/projects/shop/alerts')
    await rows()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Delete rule Quiet' }))
    let dialog = await screen.findByRole('dialog', { name: 'Delete this alert rule?' })
    expect(dialog.textContent).not.toContain(DELETE_FIRING)
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Delete rule High error rate' }))
    dialog = await screen.findByRole('dialog', { name: 'Delete this alert rule?' })
    expect(dialog.textContent).toContain(DELETE_FIRING)
    await user.click(within(dialog).getByRole('button', { name: 'Delete rule' }))
    await waitFor(() => expect(requests.some((r) => r.method === 'DELETE' && r.path === `${B}/rules/r1`)).toBe(true))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('moves through rows with j and k and opens a rule with Enter', async () => {
    mockAlerts({ [`GET ${B}/rules`]: () => json(200, { rules: RULES }), [`GET ${B}/rules/r2`]: () => json(200, { rule: RULES[1] }) })
    renderApp('/projects/shop/alerts')
    const r = await rows()
    const user = userEvent.setup()
    ;(document.activeElement as HTMLElement | null)?.blur()
    await user.keyboard('j')
    expect(r[0]?.className).toBe('selected')
    expect(document.activeElement).toBe(within(r[0] as HTMLElement).getByRole('link'))
    await user.keyboard('jjk')
    expect(r[1]?.className).toBe('selected')
    ;(document.activeElement as HTMLElement).blur()
    await user.keyboard('{Enter}')
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/projects/shop/alerts/rules/r2'))
  })

  it('renders every user string as text (XSS payloads)', async () => {
    mockAlerts({
      [`GET ${B}/rules`]: () => json(200, { rules: [rule('r1', { name: XSS, note: XSS, params: { threshold_percent: 5, min_logs: 50, services: [XSS] } })] }),
      [`GET ${B}/channels`]: () => json(200, { channels: [channel('c1', { name: XSS, url_host: `https://${XSS}` })] }),
      [`GET ${B}/events`]: () => json(200, { events: [event(1, { rule_name: XSS, template: XSS })], next_before: null }),
      [`GET ${B}/notifications`]: () => json(200, { notifications: [], next_before: null }),
    })
    renderApp('/projects/shop/alerts')
    const r = await rows()
    expect(r[0]?.textContent).toContain(XSS)
    const user = userEvent.setup()
    await user.click(screen.getByRole('tab', { name: 'Channels' }))
    await screen.findByRole('table', { name: 'Notification channels of this project' })
    await user.click(screen.getByRole('tab', { name: 'Activity' }))
    await screen.findByRole('table', { name: /State changes/ })
    expect(document.querySelector('img[src="x"]')).toBeNull()
    expect(document.querySelectorAll('script').length).toBe(0)
    expect((window as { __xss?: number }).__xss).toBeUndefined()
    expect(document.body.textContent).toContain(XSS)
  })

  it('opens the create dialog from ?create=rule once, for owners only', async () => {
    mockAlerts({})
    renderApp('/projects/shop/alerts?create=rule')
    expect(await screen.findByRole('dialog', { name: 'Create an alert rule' })).toBeTruthy()
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/projects/shop/alerts'))
  })

  it('does not open a create dialog for viewers', async () => {
    mockAlerts({}, 'viewer')
    renderApp('/projects/shop/alerts?tab=channels&create=channel')
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/projects/shop/alerts?tab=channels'))
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})
