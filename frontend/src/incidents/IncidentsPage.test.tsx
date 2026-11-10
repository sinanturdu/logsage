import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { json, renderApp } from '../test/util'
import { EMPTY_FILTERED, EMPTY_NONE, VIEWER_INCIDENTS } from './IncidentsPage'
import { B, XSS, incident, mockIncidents } from './testing'

afterEach(() => {
  vi.unstubAllGlobals()
  delete (window as { __xss?: number }).__xss
})

const table = () => screen.findByRole('table', { name: 'Incidents, most recently active first' })
const rows = async () => within(await table()).getAllByRole('row').slice(1)

describe('incident list', () => {
  it('shows number, title, severity and status marks, services, assignee, times and link counts', async () => {
    mockIncidents({
      incidents: [
        incident(12, { title: 'Payment errors in payments', severity: 'critical', alert_count: 2, group_count: 3, assignee: 'u-bob', assignee_email: 'bob@example.com' }),
        incident(11, { status: 'acknowledged', all_alerts_resolved: true, recurrence_of: 4, services: [] }),
      ],
    })
    renderApp('/projects/shop/incidents')
    const r = await rows()
    const cells = (i: number) => within(r[i] as HTMLElement).getAllByRole('cell').map((c) => c.textContent)
    expect(cells(0)).toEqual(['#12', 'Payment errors in payments', '▰▰▰▰ critical', '▶ open', 'payments', 'bob@example.com', expect.any(String), expect.any(String), '2', '3'])
    expect(cells(1)[1]).toBe('Incident 11All alerts resolved (recurrence of #4)')
    expect(cells(1)[3]).toBe('◧ acknowledged')
    expect(cells(1)[4]).toBe('all')
    expect(cells(1)[5]).toBe('nobody')
    expect(within(r[1] as HTMLElement).getByRole('link', { name: '#4' }).getAttribute('href')).toBe('/projects/shop/incidents/4')
    expect(within(r[0] as HTMLElement).getByRole('link', { name: 'Payment errors in payments' }).getAttribute('href')).toBe('/projects/shop/incidents/12')
    expect(r[0]?.querySelectorAll('time')[0]?.getAttribute('title')).toBe('2026-11-10 12:00:00 UTC')
    expect(screen.getByText('Showing 2 incidents, 1 open.')).toBeTruthy()
  })

  it('keeps the filters in the URL and asks the API with its names', async () => {
    const { requests } = mockIncidents({ incidents: [incident(1, { status: 'resolved' })] })
    renderApp('/projects/shop/incidents')
    await screen.findByText(EMPTY_FILTERED) // the default hides resolved ones
    expect(requests.filter((r) => r.path === B).at(-1)?.params.get('status')).toBe('open,acknowledged')
    const user = userEvent.setup()
    await user.selectOptions(screen.getByLabelText('Status'), 'resolved')
    await user.click(screen.getByRole('checkbox', { name: 'critical' }))
    await user.selectOptions(screen.getByLabelText('Assignee'), 'unassigned')
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/projects/shop/incidents?status=resolved&severity=critical&assignee=unassigned'))
    const last = requests.filter((r) => r.path === B).at(-1)
    expect(last?.params.get('status')).toBe('resolved')
    expect(last?.params.get('severity')).toBe('critical')
    expect(last?.params.get('assignee')).toBe('none')
  })

  it('loads more with the cursor and never shows an incident twice', async () => {
    const many = Array.from({ length: 51 }, (_, i) => incident(60 - i))
    const { routes } = mockIncidents({ incidents: many })
    let calls = 0
    const base = routes[`GET ${B}`]
    routes[`GET ${B}`] = async (r) => {
      calls++
      const res = await base?.(r)
      if (calls === 2 && res) {
        // Activity moved #11 onto the next page too.
        const body = (await res.json()) as { incidents: unknown[]; next_cursor: null }
        return json(200, { incidents: [many[49], ...body.incidents], next_cursor: null })
      }
      return res as Response
    }
    renderApp('/projects/shop/incidents')
    expect((await rows()).length).toBe(50)
    await userEvent.setup().click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(async () => expect((await rows()).length).toBe(51))
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
  })

  it('moves with j and k, opens with Enter and focuses the search with /', async () => {
    mockIncidents({ incidents: [incident(3), incident(2), incident(1)] })
    renderApp('/projects/shop/incidents')
    const r = await rows()
    const user = userEvent.setup()
    ;(document.activeElement as HTMLElement | null)?.blur()
    await user.keyboard('jj')
    expect(r[1]?.className).toBe('selected')
    await user.keyboard('k')
    expect(r[0]?.className).toBe('selected')
    expect(document.activeElement).toBe(within(r[0] as HTMLElement).getByRole('link', { name: 'Incident 3' }))
    ;(document.activeElement as HTMLElement).blur()
    await user.keyboard('/')
    expect(document.activeElement).toBe(screen.getByLabelText('Search titles'))
    ;(document.activeElement as HTMLElement).blur()
    await user.keyboard('{Enter}')
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/projects/shop/incidents/3'))
  })

  it('creates an incident with counters, server field errors and limits', async () => {
    const { requests } = mockIncidents({ incidents: [] })
    renderApp('/projects/shop/incidents')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Create incident' }))
    const dialog = await screen.findByRole('dialog', { name: 'Create an incident' })
    expect(document.activeElement).toBe(within(dialog).getByLabelText('Title'))
    await user.click(within(dialog).getByRole('button', { name: 'Create incident' }))
    expect(dialog.textContent).toContain('Enter a title')
    expect(requests.some((r) => r.method === 'POST')).toBe(false)
    const title = within(dialog).getByLabelText('Title')
    await user.click(title)
    await user.paste('x'.repeat(201))
    expect(dialog.textContent).toContain('201 of 200 characters (1 too many)')
    await user.clear(title)
    await user.type(title, 'Checkout down')
    expect(dialog.textContent).toContain('13 of 200 characters')
    await user.selectOptions(within(dialog).getByLabelText('Severity'), 'critical')
    await user.click(within(dialog).getByRole('button', { name: 'Create incident' }))
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/projects/shop/incidents/1'))
    expect(requests.find((r) => r.method === 'POST')?.body).toEqual({ title: 'Checkout down', severity: 'critical', summary: '' })
  })

  it('shows a server field error next to its field', async () => {
    mockIncidents({ incidents: [], routes: { [`POST ${B}`]: () => json(400, { error: 'summary: at most 2000 characters' }) } })
    renderApp('/projects/shop/incidents')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Create incident' }))
    const dialog = await screen.findByRole('dialog', { name: 'Create an incident' })
    await user.type(within(dialog).getByLabelText('Title'), 'x')
    await user.click(within(dialog).getByRole('button', { name: 'Create incident' }))
    const summary = within(dialog).getByLabelText('Summary (optional)')
    await waitFor(() => expect(summary.getAttribute('aria-invalid')).toBe('true'))
    expect(dialog.textContent).toContain('At most 2000 characters.')
  })

  it('has two empty states', async () => {
    mockIncidents({ incidents: [] })
    renderApp('/projects/shop/incidents')
    expect(await screen.findByText(EMPTY_NONE)).toBeTruthy()
  })

  it('is read-only for viewers', async () => {
    mockIncidents({ incidents: [incident(1)], role: 'viewer' })
    renderApp('/projects/shop/incidents')
    await rows()
    expect(screen.getByText(VIEWER_INCIDENTS)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Create incident' })).toBeNull()
  })

  it('offers Try again on errors and says when to retry on 429', async () => {
    const { routes } = mockIncidents({ incidents: [incident(1)] })
    const ok = routes[`GET ${B}`]
    routes[`GET ${B}`] = () => json(429, { error: 'slow' }, { 'Retry-After': '12' })
    renderApp('/projects/shop/incidents')
    const panel = await screen.findByRole('alert')
    expect(panel.textContent).toContain('Try again in 12 seconds.')
    routes[`GET ${B}`] = ok as NonNullable<typeof ok>
    await userEvent.setup().click(within(panel).getByRole('button', { name: 'Try again' }))
    expect((await rows()).length).toBe(1)
  })

  it('renders every user string as text (XSS payloads)', async () => {
    mockIncidents({ incidents: [incident(1, { title: XSS, services: [XSS], assignee: 'u-x', assignee_email: XSS })] })
    renderApp('/projects/shop/incidents')
    const r = await rows()
    expect(r[0]?.textContent?.split(XSS).length).toBe(4)
    expect(document.querySelector('img[src="x"]')).toBeNull()
    expect((window as { __xss?: number }).__xss).toBeUndefined()
  })
})
