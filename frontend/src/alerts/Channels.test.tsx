import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { COPY_FALLBACK } from '../explorer/RowDetail'
import { json, renderApp, settle } from '../test/util'
import { NOT_ALLOWED_ADDRESS, URL_CREDENTIALS } from './api'
import { SAMPLE_WARNING, SECRET_ONCE } from './ChannelDialogs'
import { channel, delivery, mockAlerts, rule } from './testing'

const SECRET = 'lsw_Zq8Xv2Lm4Nb7Kc1Pd9Rf3Th6Wj0Ys5Ga'
const B = '/api/projects/shop/alerts'
const CHANNELS = [channel('c1', { name: 'Slack ops', kind: 'slack', url_host: 'https://hooks.slack.com', include_sample: true }), channel('c2', { name: 'pager' })]

let log: ReturnType<typeof vi.spyOn>
let err: ReturnType<typeof vi.spyOn>
beforeEach(() => {
  log = vi.spyOn(console, 'log')
  err = vi.spyOn(console, 'error')
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  localStorage.clear()
  sessionStorage.clear()
})

async function channelsTab(routes: Parameters<typeof mockAlerts>[0] = {}, role: 'owner' | 'admin' | 'viewer' = 'owner') {
  const m = mockAlerts({ [`GET ${B}/channels`]: () => json(200, { channels: CHANNELS }), ...routes }, role)
  const r = renderApp('/projects/shop/alerts?tab=channels')
  const table = await screen.findByRole('table', { name: 'Notification channels of this project' })
  return { ...m, ...r, table, user: userEvent.setup() }
}

describe('channels', () => {
  it('lists type, name, host only, last delivery and the sample setting', async () => {
    const { table } = await channelsTab({
      [`GET ${B}/notifications`]: (r) =>
        json(200, {
          notifications: r.params.get('channel') === 'c1' ? [delivery(5, { status: 'failed', response_status: 500 })] : [],
          next_before: null,
        }),
    })
    const rows = within(table).getAllByRole('row').slice(1)
    await waitFor(() => expect(rows[0]?.textContent).toContain('✕ failed (HTTP 500)'))
    expect(within(rows[0] as HTMLElement).getAllByRole('cell').map((c) => c.textContent?.split(',')[0])).toEqual([
      'Slack',
      'Slack ops',
      'https://hooks.slack.com',
      '✕ failed (HTTP 500)',
      'Included',
      expect.any(String),
    ])
    await waitFor(() => expect(rows[1]?.textContent).toContain('none yet'))
    expect(rows[1]?.textContent).toContain('Not included')
    expect(within(rows[0] as HTMLElement).queryByRole('button', { name: /Rotate/ })).toBeNull() // Slack has no secret
    expect(within(rows[1] as HTMLElement).getByRole('button', { name: 'Rotate the secret of pager' })).toBeTruthy()
  })

  it('shows the new webhook secret once, then nowhere: not in the DOM, cache, storage, URL or console', async () => {
    const { user, client, requests } = await channelsTab({
      [`POST ${B}/channels`]: () => json(201, { channel: channel('c3', { name: 'hook' }), secret: SECRET }),
    })
    const opener = screen.getByRole('button', { name: 'Create channel' })
    await user.click(opener)
    const dialog = await screen.findByRole('dialog', { name: 'Create a channel' })
    const sample = within(dialog).getByRole('switch', { name: 'Include one sample log message' }) as HTMLInputElement
    expect(sample.checked).toBe(false)
    expect(document.getElementById(sample.getAttribute('aria-describedby') ?? '')?.textContent).toBe(SAMPLE_WARNING)
    await user.type(within(dialog).getByLabelText('Name'), 'hook')
    await user.type(within(dialog).getByLabelText('URL'), 'https://hooks.example.com/x')
    await user.click(within(dialog).getByRole('button', { name: 'Create channel' }))

    const reveal = await screen.findByRole('dialog', { name: 'Your new webhook channel' })
    expect(requests.find((r) => r.method === 'POST')?.body).toEqual({ name: 'hook', kind: 'webhook', url: 'https://hooks.example.com/x', include_sample: false })
    expect(reveal.textContent).toContain(SECRET_ONCE)
    expect(within(reveal).getByTestId('signing-secret').textContent).toBe(SECRET)
    expect(within(reveal).getByRole('textbox', { name: 'Signing secret' }).getAttribute('aria-readonly')).toBe('true')
    // The examples use a placeholder, never the secret.
    await user.click(within(reveal).getByText('How to verify the signature'))
    for (const pre of reveal.querySelectorAll('pre')) expect(pre.textContent).not.toContain(SECRET)
    expect(reveal.textContent).toContain('X-LogSage-Signature')

    await user.keyboard('{Escape}')
    expect(screen.getByRole('dialog', { name: 'Your new webhook channel' })).toBeTruthy()
    const done = within(reveal).getByRole('button', { name: 'Done' })
    expect(done.hasAttribute('disabled')).toBe(true)
    await user.click(within(reveal).getByLabelText('I copied the secret'))
    await user.click(done)

    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(opener)
    expect(document.body.textContent).not.toContain(SECRET)
    expect(JSON.stringify(client.getQueryCache().getAll().map((q) => q.state.data))).not.toContain(SECRET)
    expect(JSON.stringify(client.getMutationCache().getAll().map((m) => m.state.data))).not.toContain(SECRET)
    expect(JSON.stringify({ ...localStorage })).not.toContain(SECRET)
    expect(JSON.stringify({ ...sessionStorage })).not.toContain(SECRET)
    expect(screen.getByTestId('location').textContent).not.toContain(SECRET)
    for (const spy of [log, err]) expect(JSON.stringify(spy.mock.calls)).not.toContain(SECRET)
  })

  it('copies the secret, with a fallback message when copying fails', async () => {
    const { user } = await channelsTab({ [`POST ${B}/channels/c2/rotate-secret`]: () => json(200, { channel: CHANNELS[1], secret: SECRET }) })
    await user.click(screen.getByRole('button', { name: 'Rotate the secret of pager' }))
    const confirm = await screen.findByRole('dialog', { name: 'Rotate the signing secret?' })
    expect(document.activeElement?.textContent).toBe('Cancel')
    await user.click(within(confirm).getByRole('button', { name: 'Rotate secret' }))
    const reveal = await screen.findByRole('dialog', { name: 'Your new signing secret' })
    const write = vi.spyOn(navigator.clipboard, 'writeText')
    await user.click(within(reveal).getByRole('button', { name: 'Copy' }))
    expect(write).toHaveBeenCalledWith(SECRET)
    expect(within(reveal).getByRole('status').textContent).toBe('Copied the secret.')
    const clip = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    Object.defineProperty(document, 'execCommand', { value: () => false, configurable: true })
    await user.click(within(reveal).getByRole('button', { name: 'Copy' }))
    expect(within(reveal).getByRole('status').textContent).toBe(COPY_FALLBACK)
    Reflect.deleteProperty(document, 'execCommand')
    if (clip) Object.defineProperty(navigator, 'clipboard', clip)
    else Reflect.deleteProperty(navigator, 'clipboard')
  })

  it('maps SSRF and credential refusals to plain sentences next to the URL', async () => {
    let answer = 'url: host resolves to a private or reserved address'
    const { user } = await channelsTab({ [`POST ${B}/channels`]: () => json(400, { error: answer }) })
    await user.click(screen.getByRole('button', { name: 'Create channel' }))
    const dialog = await screen.findByRole('dialog', { name: 'Create a channel' })
    await user.type(within(dialog).getByLabelText('Name'), 'meta')
    const url = within(dialog).getByLabelText('URL')
    await user.type(url, 'http://169.254.169.254/')
    await user.click(within(dialog).getByRole('button', { name: 'Create channel' }))
    await waitFor(() => expect(url.getAttribute('aria-invalid')).toBe('true'))
    expect(document.getElementById(`${url.id}-error`)?.textContent).toBe(NOT_ALLOWED_ADDRESS)
    expect(document.activeElement).toBe(url)
    answer = 'url: must not contain a user name or password'
    await user.click(within(dialog).getByRole('button', { name: 'Create channel' }))
    await waitFor(() => expect(document.getElementById(`${url.id}-error`)?.textContent).toBe(URL_CREDENTIALS))
    answer = 'url: port is not allowed'
    await user.click(within(dialog).getByRole('button', { name: 'Create channel' }))
    await waitFor(() => expect(document.getElementById(`${url.id}-error`)?.textContent).toBe(`${NOT_ALLOWED_ADDRESS} Port is not allowed.`))
  })

  it('sends a test with the result inline, and explains the 5 per 10 minutes limit on 429', async () => {
    let limited = false
    const { user, table } = await channelsTab({
      [`POST ${B}/channels/c2/test`]: () => (limited ? json(429, { error: 'too many requests' }, { 'Retry-After': '420' }) : json(202, { notification_id: 9, idempotency_key: 'k' })),
    })
    const row = within(table).getAllByRole('row')[2] as HTMLElement
    await user.click(within(row).getByRole('button', { name: 'Send a test to pager' }))
    expect((await within(row).findByRole('status')).textContent).toContain('It is marked as a test')
    limited = true
    await user.click(within(row).getByRole('button', { name: 'Send a test to pager' }))
    await waitFor(() => expect(within(row).getByRole('status').textContent).toBe('Test sends are limited to 5 per channel every 10 minutes. Try again in 7 minutes.'))
  })

  it('asks before deleting a channel and names the rules that use it', async () => {
    const { user, requests } = await channelsTab({
      [`GET ${B}/rules`]: () => json(200, { rules: [rule('r1', { name: 'Errors', channels: ['c1'] }), rule('r2', { name: 'Volume', channels: ['c1', 'c2'] }), rule('r3', { name: 'Other', channels: ['c2'] })] }),
      [`DELETE ${B}/channels/c1`]: () => json(204),
    })
    await user.click(screen.getByRole('button', { name: 'Delete channel Slack ops' }))
    const dialog = await screen.findByRole('dialog', { name: 'Delete this channel?' })
    const items = within(dialog).getAllByRole('listitem').map((li) => li.textContent)
    expect(items).toEqual(['Errors (no other channel: edit it to choose one)', 'Volume'])
    expect(dialog.textContent).toContain('These 2 rules use it')
    await user.click(within(dialog).getByRole('button', { name: 'Delete channel' }))
    await waitFor(() => expect(requests.some((r) => r.method === 'DELETE' && r.path === `${B}/channels/c1`)).toBe(true))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('edits name and sample setting without sending the URL when it is left empty', async () => {
    const { user, requests } = await channelsTab({ [`PUT ${B}/channels/c2`]: () => json(200, { channel: CHANNELS[1] }) })
    await user.click(screen.getByRole('button', { name: 'Edit channel pager' }))
    const dialog = await screen.findByRole('dialog', { name: 'Edit channel' })
    expect(dialog.textContent).toContain('Now sends to https://hooks.example.com')
    await user.click(within(dialog).getByRole('switch', { name: 'Include one sample log message' }))
    await user.click(within(dialog).getByRole('button', { name: 'Save channel' }))
    await waitFor(() => expect(requests.find((r) => r.method === 'PUT')?.body).toEqual({ name: 'pager', include_sample: true }))
  })
})

describe('test send is never retried', () => {
  it('one click is one request; after a 429 the button waits for Retry-After', async () => {
    const { table, requests, client } = await channelsTab({
      [`POST ${B}/channels/c2/test`]: () => json(429, { error: 'too many requests' }, { 'Retry-After': '45' }),
    })
    const row = within(table).getAllByRole('row')[2] as HTMLElement
    const button = within(row).getByRole('button', { name: 'Send a test to pager' })
    // The clock moves only when the test says so. fireEvent, not user-event:
    // user-event's wrapper waits on a timer that Vitest's fake clock never runs.
    vi.useFakeTimers()
    try {
      fireEvent.click(button)
      await settle(client, 'the 429 answer', () => (within(row).queryByRole('status')?.textContent ?? '').includes('Try again in 45 seconds'))
      const posts = () => requests.filter((r) => r.method === 'POST').length
      expect(posts()).toBe(1) // no automatic retry
      expect(button.hasAttribute('disabled')).toBe(true)
      fireEvent.click(button)
      expect(posts()).toBe(1)
      await act(() => vi.advanceTimersByTimeAsync(44_000))
      expect(button.hasAttribute('disabled')).toBe(true)
      await act(() => vi.advanceTimersByTimeAsync(1_000))
      expect(button.hasAttribute('disabled')).toBe(false)
      expect(posts()).toBe(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('the client sends a refused POST once, whatever the status', async () => {
    const { testChannel } = await import('./api')
    for (const status of [400, 403, 404, 409, 429, 500, 503]) {
      const f = vi.fn(async () => json(status, { error: 'x' }, { 'Retry-After': '1' }))
      vi.stubGlobal('fetch', f)
      await expect(testChannel('shop', 'c1')).rejects.toBeTruthy()
      expect(f).toHaveBeenCalledTimes(1)
    }
  })
})
