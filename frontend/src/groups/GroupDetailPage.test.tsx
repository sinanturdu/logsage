import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { event } from '../explorer/testing'
import { json, renderApp } from '../test/util'
import type { GroupDetail } from './api'
import { NEW_TEXT, VIEWER_STATUS, regressedText } from './GroupDetailPage'
import { NOTE_TOO_LONG } from './StatusDialog'
import { group, mockGroups, NOW } from './testing'

const FP = '0123456789abcdef'
const PATH = '/api/projects/shop/groups/0123456789abcdef'

const detail = (extra: Partial<GroupDetail> = {}): GroupDetail => ({
  ...group(FP, { is_new: true, count: 42, error_type: 'TimeoutError' }),
  counts: [
    { service: 'checkout', level: 'error', count: 2 },
    { service: 'payments', level: 'error', count: 40 },
  ],
  samples: [event(0, { message: 'payment gateway timeout after 5000 ms (psp-eu-2)' }), event(1)] as never,
  note: '',
  updated_by: '',
  ...extra,
})

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

/** A response the test releases when it wants (no sleeping to order events). */
function deferred() {
  let release: (r: Response) => void = () => {}
  const promise = new Promise<Response>((r) => (release = r))
  return { promise, release }
}

describe('group detail', () => {
  it('shows the template, flags in words, facts, breakdown, samples and links', async () => {
    const { requests } = mockGroups({ [`GET ${PATH}`]: () => json(200, detail({ regressed: true, status: 'resolved', resolved_at: '2026-11-07T19:00:00.000Z' })) })
    renderApp(`/projects/shop/groups/${FP}?range=1h`)
    const h1 = await screen.findByRole('heading', { level: 1 })
    expect(h1.textContent).toBe('payment gateway timeout after <n> ms')
    expect(h1.querySelector('.ph')?.textContent).toBe('<n>')
    expect(screen.getByText(NEW_TEXT)).toBeTruthy()
    expect(screen.getByText('Regressed: resolved on 2026-11-07 19:00:00 UTC and seen again after that.')).toBeTruthy()
    expect(screen.getByText('42')).toBeTruthy()
    expect(screen.getByText('TimeoutError')).toBeTruthy()
    const breakdown = screen.getByRole('table')
    expect(within(breakdown).getAllByRole('row').slice(1).map((r) => r.textContent)).toEqual(['payments■error40', 'checkout■error2'])
    expect(screen.getByText('payment gateway timeout after 5000 ms (psp-eu-2)')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Open in Logs' }).getAttribute('href')).toBe(`/projects/shop/logs?range=1h&fingerprint=${FP}`)
    expect(screen.getByRole('link', { name: 'All error groups' }).getAttribute('href')).toBe('/projects/shop/groups?range=1h')
    await waitFor(() => expect(requests.some((r) => r.path === '/api/projects/shop/histogram' && r.params.get('fingerprint') === FP)).toBe(true))
    expect(await screen.findByRole('img', { name: /^Timeline/ })).toBeTruthy()
    expect(screen.getByText(/Error share is not shown/)).toBeTruthy()
    expect(regressedText(null)).toBe('Regressed: resolved on an earlier date and seen again after that.')
  })

  it.each([
    [json(404, { error: 'not found' }), 'This group does not exist in this project.', null],
    [json(429, { error: 'slow down' }, { 'Retry-After': '3' }), 'Too many requests. Try again in 3 seconds.', 'Try again'],
    [json(500, {}), 'LogSage cannot reach its log store right now. Try again in a moment.', 'Try again'],
  ])('states (%#)', async (res, text, button) => {
    mockGroups({ [`GET ${PATH}`]: () => res.clone() })
    renderApp(`/projects/shop/groups/${FP}`)
    expect(await screen.findByText(new RegExp(text.replace(/[.?]/g, '\\$&')))).toBeTruthy()
    if (button) expect(screen.getByRole('button', { name: button })).toBeTruthy()
  })

  it('owners resolve with a note: optimistic, announced, sent to the server', async () => {
    const answer = deferred()
    const { requests } = mockGroups({
      [`GET ${PATH}`]: () => json(200, detail()),
      [`POST ${PATH}/status`]: () => answer.promise,
    })
    renderApp(`/projects/shop/groups/${FP}`)
    await screen.findByRole('heading', { level: 1 })
    const user = userEvent.setup()
    expect(screen.queryByRole('button', { name: 'Reopen' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Resolve' }))
    const dialog = screen.getByRole('dialog', { name: 'Resolve this group' })
    expect(document.activeElement).toBe(within(dialog).getByLabelText('Note (optional)'))
    await user.type(within(dialog).getByLabelText('Note (optional)'), 'fixed in 1.4.2')
    expect(within(dialog).getByText('14 of 500 characters')).toBeTruthy()
    await user.click(within(dialog).getByRole('button', { name: 'Resolve' }))
    // Before the server answers, the page already shows the new status.
    expect(screen.queryByRole('dialog')).toBeNull()
    const status = screen.getByRole('region', { name: 'Status' })
    expect(within(status).getAllByText('Resolved').length).toBeGreaterThan(0)
    expect(within(status).getByRole('button', { name: 'Reopen' })).toBeTruthy()
    answer.release(json(200, { group: { fingerprint: FP, status: 'resolved', resolved_at: '2026-11-07T20:15:00.000Z', note: 'fixed in 1.4.2', updated_by: 'alice@example.com', updated_at: '2026-11-07T20:15:00.000Z' } }))
    expect(await screen.findByText('Group resolved')).toBeTruthy()
    expect(screen.getByText('Last changed by', { exact: false }).textContent).toBe('Last changed by alice@example.com.')
    expect(screen.getByText('fixed in 1.4.2')).toBeTruthy()
    expect(requests.find((r) => r.method === 'POST')?.body).toEqual({ status: 'resolved', note: 'fixed in 1.4.2' })
  })

  it('rolls back when the server refuses, and says why', async () => {
    const answer = deferred()
    mockGroups({ [`GET ${PATH}`]: () => json(200, detail()), [`POST ${PATH}/status`]: () => answer.promise }, 'admin')
    renderApp(`/projects/shop/groups/${FP}`)
    await screen.findByRole('heading', { level: 1 })
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Ignore' }))
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Ignore' }))
    const status = screen.getByRole('region', { name: 'Status' })
    expect(within(status).getAllByText('Ignored').length).toBeGreaterThan(0)
    answer.release(json(500, {}))
    expect(await screen.findByText(/The status was not changed\. The LogSage API failed \(status 500\)/)).toBeTruthy()
    expect(within(status).queryAllByText('Ignored')).toHaveLength(0)
    expect(within(status).getAllByText('Open').length).toBeGreaterThan(0)
    expect(screen.queryByText('Group ignored')).toBeNull()
  })

  it('the dialog refuses a note over the limit, and Escape closes it', async () => {
    const { requests } = mockGroups({ [`GET ${PATH}`]: () => json(200, detail({ status: 'resolved' })) })
    renderApp(`/projects/shop/groups/${FP}`)
    await screen.findByRole('heading', { level: 1 })
    const user = userEvent.setup()
    const reopen = screen.getByRole('button', { name: 'Reopen' })
    await user.click(reopen)
    const field = within(screen.getByRole('dialog')).getByLabelText('Note (optional)')
    await user.click(field)
    await user.paste('x'.repeat(501))
    expect(screen.getByText(NOTE_TOO_LONG)).toBeTruthy()
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Reopen' }))
    expect(screen.getByRole('dialog')).toBeTruthy()
    expect(requests.some((r) => r.method === 'POST')).toBe(false)
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(reopen)
  })

  it('viewers see the status read-only, with why', async () => {
    mockGroups({ [`GET ${PATH}`]: () => json(200, detail({ status: 'resolved', note: 'known issue', updated_by: 'bob@example.com' })) }, 'viewer')
    renderApp(`/projects/shop/groups/${FP}`)
    await screen.findByRole('heading', { level: 1 })
    expect(screen.getByText(VIEWER_STATUS)).toBeTruthy()
    for (const name of ['Resolve', 'Ignore', 'Reopen']) expect(screen.queryByRole('button', { name })).toBeNull()
    expect(screen.getByText('known issue')).toBeTruthy()
  })

  it('renders a hostile template, note and samples as text', async () => {
    const xss = '<img src=x onerror="window.pwned=1"><script>window.pwned=1</script>'
    mockGroups({
      [`GET ${PATH}`]: () =>
        json(200, detail({ title: xss, note: xss, updated_by: xss, error_type: xss, services: [xss], samples: [event(0, { message: xss, service: xss })] as never })),
    })
    renderApp(`/projects/shop/groups/${FP}`)
    await screen.findByRole('heading', { level: 1 })
    expect(document.querySelector('main img, main script')).toBeNull()
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe(xss)
    expect(screen.getAllByText(xss).length).toBeGreaterThan(3)
    expect((window as unknown as { pwned?: number }).pwned).toBeUndefined()
  })
})
