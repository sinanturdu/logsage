import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { json, renderApp } from '../test/util'
import { STALE } from './api'
import { ALL_RESOLVED_PROMPT, DROPPED, SUGGESTION_NOTE, VIEWER_DETAIL } from './IncidentDetailPage'
import { B, ME, XSS, ev, incident, linkedAlert, linkedGroup, mockIncidents, suggestion } from './testing'

afterEach(() => {
  vi.unstubAllGlobals()
  delete (window as { __xss?: number }).__xss
})

const FP_A = 'aaaaaaaaaaaaaaaa'
const FP_M = 'abcdef0123456789'
const groupDetail = (title: string, count: number) => () =>
  json(200, { fingerprint: FP_M, title, sample: '', error_type: '', levels: ['error'], services: ['payments'], count, first_seen: '', last_seen: '', sparkline: [], is_new: false, status: 'open', regressed: false, resolved_at: null, counts: [], samples: [], note: '', updated_by: '', took_ms: 1 })

function setup(opts: Parameters<typeof mockIncidents>[0] = {}) {
  return mockIncidents({
    incidents: [incident(7, { title: 'Payment errors in payments', recurrence_of: 3, assignee: 'u-bob', assignee_email: 'bob@example.com', summary: 'PSP eu-2 slow', alert_count: 2, group_count: 2 })],
    detail: {
      alerts: [linkedAlert(), linkedAlert({ rule_id: 'r2', rule_name: 'New groups', state_key: `fp:${FP_A}`, state: '', closed_at: '2026-11-10T12:20:00Z', close_reason: 'rule_disabled' })],
      groups: [linkedGroup(FP_A, { template: 'gateway timeout after <n> ms' }), linkedGroup(FP_M, { source: 'manual', template: '' })],
      suggested_groups: [suggestion('1111111111111111', { is_new: true }), suggestion('2222222222222222', { title: 'db pool exhausted', regressed: true })],
      events: [
        ev(5, 'alert_firing', { rule_id: 'r1', rule_name: 'Payment errors', rule_type: 'error_rate', key: 'rule', value: 40, threshold: 20 }, { count: 5, last_at: '2026-11-10T12:25:00Z' }),
        ev(4, 'comment', {}, { actor: 'u-bob', actor_email: 'bob@example.com', body: 'Looking at it' }),
        ev(3, 'opened', { source: 'auto', rule_id: 'r1', rule_name: 'Payment errors', recurrence_of: 3 }),
      ],
    },
    ...opts,
    routes: { [`GET /api/projects/shop/groups/${FP_M}`]: groupDetail('card network slow', 42), [`GET /api/projects/shop/groups/${FP_A}`]: groupDetail('gateway timeout after <n> ms', 900), ...opts.routes },
  })
}
const heading = () => screen.findByRole('heading', { level: 1 })

describe('incident detail', () => {
  it('shows the header, links, sections, suggestions with the honest sentence, and the timeline', async () => {
    setup()
    renderApp('/projects/shop/incidents/7')
    expect((await heading()).textContent).toBe('#7 Payment errors in payments')
    expect(document.querySelector('.incident-marks')?.textContent).toContain('▰▰▰▱ high ▶ open Recurrence of #3.')
    expect(screen.getByRole('link', { name: '#3' }).getAttribute('href')).toBe('/projects/shop/incidents/3')
    expect(document.querySelector('.facts')?.textContent).toContain('bob@example.com')
    expect(screen.getByText('PSP eu-2 slow')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Open in Logs' }).getAttribute('href')).toBe('/projects/shop/logs?from=2026-11-10T11%3A45%3A00Z&to=2026-11-10T12%3A31%3A00Z')

    const alerts = within(screen.getByRole('table', { name: 'Alert states linked to this incident' })).getAllByRole('row').slice(1)
    expect(within(alerts[0] as HTMLElement).getByRole('link', { name: 'Payment errors' }).getAttribute('href')).toBe('/projects/shop/alerts/rules/r1')
    expect(alerts[0]?.textContent).toContain('◉ firing')
    expect(alerts[1]?.textContent).toContain('gateway timeout after <n> ms')
    expect(alerts[1]?.textContent).toContain('rule disabled')

    // A manually linked group has no stored template: it is fetched lazily, with its count.
    const groups = within(screen.getByRole('table', { name: 'Error groups linked to this incident' })).getAllByRole('row').slice(1)
    await waitFor(() => expect(groups[1]?.textContent).toContain('card network slow'))
    expect(groups[1]?.textContent).toContain('42')
    expect(groups[1]?.textContent).toContain('by hand')
    expect(within(groups[0] as HTMLElement).getByRole('link').getAttribute('href')).toBe(`/projects/shop/groups/${FP_A}`)

    expect(screen.getByText(SUGGESTION_NOTE)).toBeTruthy()
    expect(SUGGESTION_NOTE).toBe('Suggestions are the biggest error groups on the affected services during this incident. They are not confirmed causes.')
    const sugg = document.querySelector('.suggestions') as HTMLElement
    expect(sugg.textContent).toContain('New')
    expect(sugg.textContent).toContain('Regressed')
    expect(document.body.textContent?.toLowerCase()).not.toMatch(/root cause|caused by/)

    const tl = Array.from(document.querySelectorAll('.incident-timeline .tl-line')).map((l) => l.textContent?.split(' just now')[0]?.split(/ \d+ (min|h|days) ago/)[0])
    expect(tl[0]).toContain('Rule “Payment errors” fired 5 times, most recently at 12:25 UTC.')
    expect(tl[1]).toContain('bob@example.com commented:')
    expect(tl[2]).toContain('Rule “Payment errors” opened this incident. It is a recurrence of #3.')
    expect(screen.getByText('Looking at it')).toBeTruthy()
  })

  it('acknowledges with a note, optimistically, and announces it', async () => {
    const { requests } = setup()
    renderApp('/projects/shop/incidents/7')
    await heading()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Acknowledge' }))
    const dialog = await screen.findByRole('dialog', { name: 'Acknowledge this incident?' })
    expect(document.activeElement).toBe(within(dialog).getByLabelText('Note (optional, added as a comment)'))
    await user.type(within(dialog).getByLabelText('Note (optional, added as a comment)'), 'On it')
    await user.click(within(dialog).getByRole('button', { name: 'Acknowledge' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(requests.find((r) => r.path === `${B}/7/acknowledge`)?.body).toEqual({ version: 1, note: 'On it' })
    await waitFor(() => expect(document.querySelector('.incident-marks')?.textContent).toContain('◧ acknowledged'))
    expect(screen.getAllByRole('status').some((s) => s.textContent === 'Incident acknowledged.')).toBe(true)
    await waitFor(() => expect(screen.getByText('On it')).toBeTruthy())
  })

  it('rolls the optimistic status back when the change fails', async () => {
    setup({ routes: { [`POST ${B}/7/resolve`]: () => json(503, { error: 'service unavailable' }) } })
    renderApp('/projects/shop/incidents/7')
    await heading()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Resolve' }))
    const dialog = await screen.findByRole('dialog', { name: 'Resolve this incident?' })
    await user.click(within(dialog).getByRole('button', { name: 'Resolve' }))
    await waitFor(() => expect(dialog.textContent).toContain('The LogSage API failed (status 503)'))
    expect(document.querySelector('.incident-marks')?.textContent).toContain('▶ open')
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('on 409 keeps what the user typed and offers a reload', async () => {
    const { state } = setup()
    renderApp('/projects/shop/incidents/7')
    await heading()
    const first = state.incidents[0]
    if (first) first.version = 4 // someone else changed it
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Edit' }))
    const dialog = await screen.findByRole('dialog', { name: 'Edit incident' })
    const title = within(dialog).getByLabelText('Title')
    await user.clear(title)
    await user.type(title, 'My careful new title')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await within(dialog).findByText(STALE)
    expect((within(dialog).getByLabelText('Title') as HTMLInputElement).value).toBe('My careful new title')
    await user.click(within(dialog).getByRole('button', { name: 'Reload' }))
    expect((within(dialog).getByLabelText('Title') as HTMLInputElement).value).toBe('My careful new title')
    await waitFor(async () => {
      await user.click(within(dialog).getByRole('button', { name: 'Save' }))
      expect(screen.queryByRole('dialog')).toBeNull()
    })
    expect((await heading()).textContent).toBe('#7 My careful new title')
  })

  it('offers to resolve when every linked alert is resolved, never by itself', async () => {
    const { requests } = setup({ incidents: [incident(7, { all_alerts_resolved: true })] })
    renderApp('/projects/shop/incidents/7')
    await heading()
    const prompt = screen.getByText(ALL_RESOLVED_PROMPT).closest('.notice') as HTMLElement
    expect(requests.some((r) => r.method === 'POST')).toBe(false)
    await userEvent.setup().click(within(prompt).getByRole('button', { name: 'Resolve' }))
    expect(await screen.findByRole('dialog', { name: 'Resolve this incident?' })).toBeTruthy()
  })

  it('assigns from the members, to me, and unassigns', async () => {
    const { requests } = setup()
    renderApp('/projects/shop/incidents/7')
    await heading()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Assign to me' }))
    await waitFor(() => expect(requests.find((r) => r.path === `${B}/7/assign`)?.body).toEqual({ user_id: ME }))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Assign to me' })).toBeNull())
    await user.selectOptions(screen.getByLabelText('Assignee'), '')
    await waitFor(() => expect(requests.some((r) => r.path === `${B}/7/unassign`)).toBe(true))
  })

  it('links a suggestion and unlinks a group', async () => {
    const { requests } = setup()
    renderApp('/projects/shop/incidents/7')
    await heading()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Link group db pool exhausted' }))
    await waitFor(() => expect(requests.some((r) => r.method === 'POST' && r.path === `${B}/7/groups/2222222222222222`)).toBe(true))
    await user.click(screen.getByRole('button', { name: 'Unlink group gateway timeout after <n> ms' }))
    await waitFor(() => expect(requests.some((r) => r.method === 'DELETE' && r.path === `${B}/7/groups/${FP_A}`)).toBe(true))
  })

  it('adds and deletes comments; the event cap is explained', async () => {
    const { requests, state } = setup()
    renderApp('/projects/shop/incidents/7')
    await heading()
    const user = userEvent.setup()
    const box = screen.getByLabelText('Add a comment')
    await user.type(box, 'PSP back')
    expect(document.body.textContent).toContain('8 of 2000 characters')
    await user.click(screen.getByRole('button', { name: 'Add comment' }))
    await waitFor(() => expect(screen.getByText('PSP back')).toBeTruthy())
    await user.click(screen.getByRole('button', { name: 'Delete the comment by bob@example.com' }))
    const dialog = await screen.findByRole('dialog', { name: 'Delete this comment?' })
    expect(document.activeElement?.textContent).toBe('Cancel')
    await user.click(within(dialog).getByRole('button', { name: 'Delete comment' }))
    await waitFor(() => expect(requests.some((r) => r.method === 'DELETE' && r.path === `${B}/7/comments/4`)).toBe(true))
    state.eventCap.add(7)
    await user.type(screen.getByLabelText('Add a comment'), 'one more')
    await user.click(screen.getByRole('button', { name: 'Add comment' }))
    expect(await screen.findByText('This incident has reached its event limit. New comments cannot be added.')).toBeTruthy()
  })

  it('is read-only for viewers, without a comment form', async () => {
    setup({ role: 'viewer' })
    renderApp('/projects/shop/incidents/7')
    await heading()
    expect(screen.getByText(VIEWER_DETAIL)).toBeTruthy()
    expect(screen.getByText('Only owners and admins can comment.')).toBeTruthy()
    for (const name of ['Acknowledge', 'Resolve', 'Edit', 'Assign to me', 'Add comment', 'Link group db pool exhausted', 'Delete the comment by bob@example.com']) {
      expect(screen.queryByRole('button', { name })).toBeNull()
    }
    expect(screen.queryByLabelText('Assignee')).toBeNull()
  })

  it('loads older timeline events and says when old events were dropped', async () => {
    const events = Array.from({ length: 60 }, (_, i) => ev(100 - i, 'all_alerts_resolved'))
    setup({ incidents: [incident(7, { events_dropped: 12 })], detail: { events } })
    renderApp('/projects/shop/incidents/7')
    await heading()
    expect(document.querySelectorAll('.incident-timeline li').length).toBe(50)
    expect(screen.queryByText(DROPPED)).toBeNull()
    await userEvent.setup().click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(document.querySelectorAll('.incident-timeline li').length).toBe(60))
    expect(screen.getByText(DROPPED)).toBeTruthy()
  })

  it('answers a clear 404 for unknown or malformed numbers, and retries after errors', async () => {
    setup()
    renderApp('/projects/shop/incidents/99')
    expect((await screen.findByRole('alert')).textContent).toContain('This incident does not exist in this project.')
  })

  it.each(['0', 'abc', '-1', '1.5'])('a malformed number %s is a 404 page', async (n) => {
    setup()
    renderApp(`/projects/shop/incidents/${n}`)
    expect((await screen.findByRole('heading', { level: 1 })).textContent).toBe('Incident not found')
  })

  it('says when to retry after a 429', async () => {
    const { routes } = setup({ routes: { [`GET ${B}/7`]: () => json(429, { error: 'slow' }, { 'Retry-After': '20' }) } })
    void routes
    renderApp('/projects/shop/incidents/7')
    expect((await screen.findByRole('alert')).textContent).toContain('Try again in 20 seconds.')
  })

  it('renders every user string as text (XSS payloads)', async () => {
    mockIncidents({
      incidents: [incident(7, { title: XSS, summary: XSS, services: [XSS], assignee: 'u-x', assignee_email: XSS })],
      detail: {
        alerts: [linkedAlert({ rule_name: XSS, state_key: `fp:${FP_A}` })],
        groups: [linkedGroup(FP_A, { template: XSS })],
        suggested_groups: [suggestion('1111111111111111', { title: XSS, services: [XSS] })],
        events: [ev(2, 'comment', {}, { actor: 'u-x', actor_email: XSS, body: XSS }), ev(1, 'alert_firing', { rule_name: XSS, template: XSS })],
      },
    })
    renderApp('/projects/shop/incidents/7')
    await heading()
    expect(document.querySelector('img[src="x"]')).toBeNull()
    expect(document.querySelectorAll('script').length).toBe(0)
    expect((window as { __xss?: number }).__xss).toBeUndefined()
    expect(document.body.textContent?.split(XSS).length).toBeGreaterThan(9)
  })
})
