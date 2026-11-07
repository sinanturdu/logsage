import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { alice, json, mockApi, project, renderApp } from '../test/util'
import { COPY_FALLBACK } from '../explorer/RowDetail'
import { sortKeys, type KeyInfo } from './api'
import { EMPTY_KEYS, ONCE_TEXT, REVOKE_TEXT, VIEWER_TEXT, curlExample, dateUtc, powershellExample } from './KeysPage'

const SECRET = 'lsk_ingest_9f3Kq2Lx7Vb4Np1Rt8Wz5Yc6Hd0Ja3Me'
const key = (prefix: string, over: Partial<KeyInfo> = {}): KeyInfo => ({
  prefix,
  kind: 'ingest',
  name: `${prefix}-name`,
  created_at: '2026-11-01T10:00:00Z',
  expires_at: null,
  revoked_at: null,
  last_used_at: null,
  status: 'active',
  ...over,
})
const KEYS = [
  key('lsk_old', { status: 'revoked', revoked_at: '2026-11-03T09:00:00Z', created_at: '2026-10-01T10:00:00Z' }),
  key('lsk_new', { created_at: '2026-11-05T10:00:00Z', last_used_at: '2026-11-07T16:40:12Z' }),
  key('lsk_exp', { kind: 'query', status: 'expired', expires_at: '2026-11-02T00:00:00Z' }),
  key('lsk_mid'),
]

type Routes = Parameters<typeof mockApi>[0]
function setup(role = 'owner', extra: Routes = {}, keys: KeyInfo[] = KEYS) {
  return mockApi({
    'GET /api/auth/me': () => json(200, alice),
    'GET /api/projects': () => json(200, { projects: [{ ...project('shop', 'Shop'), role }] }),
    'GET /api/projects/shop/keys': () => json(200, { keys }),
    ...extra,
  })
}
const table = () => screen.findByRole('table')
const bodyRows = (t: HTMLElement) => within(t).getAllByRole('row').slice(1)

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
  sessionStorage.clear()
})

describe('keys list', () => {
  it('lists keys active first with prefix, kind, times in UTC and status in words', async () => {
    setup()
    renderApp('/projects/shop/keys')
    const t = await table()
    const rows = bodyRows(t)
    expect(rows.map((r) => r.querySelector('td')?.textContent)).toEqual(['lsk_new', 'lsk_mid', 'lsk_exp', 'lsk_old'])
    const first = rows[0] as HTMLElement
    expect(within(first).getByText('2026-11-07 16:40 UTC').getAttribute('title')).toBe('2026-11-07T16:40:12.000Z')
    expect(first.textContent).toContain('never') // expires
    expect(rows[3]?.textContent).toContain('revoked')
    expect(rows[2]?.textContent).toContain('expired')
    expect(within(first).getByRole('button', { name: 'Revoke key lsk_new' })).toBeTruthy()
    expect(within(rows[3] as HTMLElement).queryByRole('button')).toBeNull()
    expect(screen.getByRole('link', { name: 'API keys' }).getAttribute('aria-current')).toBe('page')
  })

  it('sorts and formats deterministically', () => {
    expect(sortKeys(KEYS).map((k) => k.prefix)).toEqual(['lsk_new', 'lsk_mid', 'lsk_exp', 'lsk_old'])
    expect(dateUtc(null)).toEqual({ text: '–', title: '' })
    expect(dateUtc('2026-01-02T03:04:05Z').text).toBe('2026-01-02 03:04 UTC')
  })

  it('shows the empty state', async () => {
    setup('owner', {}, [])
    renderApp('/projects/shop/keys')
    expect(await screen.findByText(EMPTY_KEYS)).toBeTruthy()
  })

  it('shows a server error plainly, with retry', async () => {
    setup('owner', { 'GET /api/projects/shop/keys': () => json(503, {}) })
    renderApp('/projects/shop/keys')
    expect(await screen.findByText('The LogSage API failed (status 503). Try again in a minute.')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy()
  })

  it('is read-only for viewers, with a short explanation', async () => {
    setup('viewer')
    renderApp('/projects/shop/keys')
    await table()
    expect(screen.getByText(VIEWER_TEXT)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Create key' })).toBeNull()
    expect(screen.queryByRole('button', { name: /^Revoke/ })).toBeNull()
  })

  it('renders HTML payloads in key names as text', async () => {
    setup('owner', {}, [key('lsk_x', { name: '<img src=x onerror=alert(1)><script>alert(2)</script>' })])
    renderApp('/projects/shop/keys')
    const t = await table()
    expect(t.querySelector('img, script')).toBeNull()
    expect(t.textContent).toContain('<img src=x onerror=alert(1)><script>alert(2)</script>')
  })
})

describe('create key', () => {
  it('creates, reveals the key once, and forgets it after Done', async () => {
    let body: unknown
    let list = KEYS
    const calls = setup('owner', {
      'GET /api/projects/shop/keys': () => json(200, { keys: list }),
      'POST /api/projects/shop/keys': (init) => {
        body = JSON.parse(String(init?.body))
        list = [key('lsk_9f3Kq2', { name: 'checkout-prod', created_at: '2026-11-07T16:50:00Z' }), ...KEYS]
        return json(201, { key: SECRET, info: key('lsk_9f3Kq2', { name: 'checkout-prod' }) })
      },
    })
    const log = vi.spyOn(console, 'log')
    const err = vi.spyOn(console, 'error')
    const { client } = renderApp('/projects/shop/keys')
    await table()
    const user = userEvent.setup()
    const opener = screen.getByRole('button', { name: 'Create key' })
    await user.click(opener)
    const dialog = screen.getByRole('dialog', { name: 'Create an API key' })
    const ingest = within(dialog).getByRole('radio', { name: 'Ingest' })
    expect(ingest.getAttribute('aria-describedby') && document.getElementById(ingest.getAttribute('aria-describedby') ?? '')?.textContent).toBe('Sends logs to LogSage.')
    expect(within(dialog).getByRole('radio', { name: 'Query' })).toBeTruthy()
    expect(within(dialog).getByText('Reads logs through the API.')).toBeTruthy()
    expect(dialog.textContent).not.toMatch(/Ingest\s+Ingest|Query\s+Query/) // the kind name appears once
    await user.type(within(dialog).getByLabelText('Name'), 'checkout-prod')
    await user.selectOptions(within(dialog).getByLabelText('Expires'), '90 days')
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))

    const reveal = await screen.findByRole('dialog', { name: 'Your new ingest key' })
    expect(body).toEqual({ kind: 'ingest', name: 'checkout-prod', expires_in: '2160h' })
    const block = within(reveal).getByRole('textbox', { name: 'Key' })
    expect(block.textContent).toBe(SECRET) // in full, wrapping, not clipped in an input
    expect(block.getAttribute('aria-readonly')).toBe('true')
    await user.click(block)
    expect(window.getSelection()?.toString()).toBe(SECRET)
    expect(within(reveal).getByText(ONCE_TEXT)).toBeTruthy()
    const tabs = within(reveal).getAllByRole('tab')
    expect(tabs.map((t) => t.textContent)).toEqual(['macOS / Linux', 'Windows PowerShell'])
    expect(tabs[0]?.getAttribute('aria-selected')).toBe('true')
    const panel = () => within(reveal).getByRole('tabpanel')
    expect(panel().textContent).toContain("-d '{")
    tabs[0]?.focus()
    await user.keyboard('{ArrowRight}')
    expect(document.activeElement).toBe(tabs[1])
    expect(tabs[1]?.getAttribute('aria-selected')).toBe('true')
    expect(panel().textContent).toContain('curl.exe')
    for (const pre of reveal.querySelectorAll('pre')) {
      expect(pre.textContent).toContain('YOUR_KEY')
      expect(pre.textContent).not.toContain(SECRET)
    }

    // Escape does not close it; Done needs the confirmation.
    await user.keyboard('{Escape}')
    expect(screen.getByRole('dialog', { name: 'Your new ingest key' })).toBeTruthy()
    const done = within(reveal).getByRole('button', { name: 'Done' })
    expect(done.hasAttribute('disabled')).toBe(true)
    await user.click(within(reveal).getByLabelText('I copied the key'))
    await user.click(done)

    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(opener)
    expect(document.body.textContent).not.toContain(SECRET)
    expect(Array.from(document.querySelectorAll('input')).some((i) => i.value === SECRET)).toBe(false)
    expect(JSON.stringify(client.getQueryCache().getAll().map((q) => q.state.data))).not.toContain(SECRET)
    expect(JSON.stringify(client.getMutationCache().getAll().map((m) => m.state.data))).not.toContain(SECRET)
    expect(JSON.stringify({ ...localStorage })).not.toContain(SECRET)
    expect(JSON.stringify({ ...sessionStorage })).not.toContain(SECRET)
    expect(window.location.href).not.toContain(SECRET)
    expect(screen.getByTestId('location').textContent).not.toContain(SECRET)
    for (const spy of [log, err]) expect(JSON.stringify(spy.mock.calls)).not.toContain(SECRET)
    await waitFor(() => expect(bodyRows(screen.getByRole('table'))[0]?.textContent).toContain('checkout-prod'))
    expect(calls.filter((c) => c.key === 'GET /api/projects/shop/keys').length).toBeGreaterThan(1)
  })

  it('copies the key, with a fallback message when copying fails', async () => {
    setup('owner', { 'POST /api/projects/shop/keys': () => json(201, { key: SECRET, info: key('lsk_q', { kind: 'query' }) }) })
    renderApp('/projects/shop/keys')
    await table()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Create key' }))
    await user.click(screen.getByRole('radio', { name: 'Query' }))
    await user.type(screen.getByLabelText('Name'), 'reader')
    await user.click(screen.getByRole('button', { name: 'Create' }))
    const reveal = await screen.findByRole('dialog', { name: 'Your new query key' })
    expect(reveal.querySelector('pre')).toBeNull() // no curl example for query keys
    const write = vi.spyOn(navigator.clipboard, 'writeText')
    await user.click(within(reveal).getByRole('button', { name: 'Copy' }))
    expect(write).toHaveBeenCalledWith(SECRET)
    expect(within(reveal).getByRole('status').textContent).toBe('Copied the key.')
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    Object.defineProperty(document, 'execCommand', { value: () => false, configurable: true })
    await user.click(within(reveal).getByRole('button', { name: 'Copy' }))
    expect(within(reveal).getByRole('status').textContent).toBe(COPY_FALLBACK)
    Reflect.deleteProperty(document, 'execCommand')
  })

  it('requires a name and shows server errors plainly', async () => {
    setup('owner', { 'POST /api/projects/shop/keys': () => json(400, { error: 'name must be 1-200 characters' }) })
    renderApp('/projects/shop/keys')
    await table()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Create key' }))
    const dialog = screen.getByRole('dialog', { name: 'Create an API key' })
    expect(within(dialog).getByLabelText('Name').getAttribute('maxlength')).toBe('200')
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    expect(within(dialog).getByRole('alert').textContent).toBe('Enter a name, for example "checkout-prod".')
    expect(document.activeElement).toBe(within(dialog).getByLabelText('Name'))
    await user.type(within(dialog).getByLabelText('Name'), 'x')
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    expect(await within(dialog).findByText('Name must be 1-200 characters.')).toBeTruthy()
  })

  it('traps focus and closes with Escape before a key exists', async () => {
    setup()
    renderApp('/projects/shop/keys')
    await table()
    const user = userEvent.setup()
    const opener = screen.getByRole('button', { name: 'Create key' })
    await user.click(opener)
    const dialog = screen.getByRole('dialog', { name: 'Create an API key' })
    expect(dialog.contains(document.activeElement)).toBe(true)
    for (let i = 0; i < 12; i++) {
      await user.tab()
      expect(dialog.contains(document.activeElement)).toBe(true)
    }
    await user.tab({ shift: true })
    expect(dialog.contains(document.activeElement)).toBe(true)
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(opener)
  })

  it('uses a placeholder in the curl example', () => {
    const c = curlExample('shop', Date.UTC(2026, 10, 7, 16, 40, 5, 250))
    expect(c).toContain('X-API-Key: YOUR_KEY')
    expect(c).toContain('"timestamp":"2026-11-07T16:40:05Z"') // the ingest API requires one
    expect(c).toMatch(/-d '\{.*\}'$/) // a single-quoted body
    const ps = powershellExample('shop', Date.UTC(2026, 10, 7, 16, 40, 5))
    expect(ps).toContain('Set-Content -Path $file -Value $body -Encoding ascii -NoNewline')
    expect(ps).toContain('curl.exe -X POST')
    expect(ps).toContain('--data-binary "@$file"')
    expect(ps).toContain('X-API-Key: YOUR_KEY')
    expect(ps.split('\n').some((l) => /^curl\s/.test(l))).toBe(false) // never the bare curl alias
    expect(ps).toContain('"timestamp":"2026-11-07T16:40:05Z"')
  })
})

describe('revoke key', () => {
  it('confirms with the consequence, revokes and refreshes the list', async () => {
    let revoked = false
    setup('admin', {
      'GET /api/projects/shop/keys': () => json(200, { keys: revoked ? [key('lsk_new', { status: 'revoked' })] : [key('lsk_new')] }),
      'POST /api/projects/shop/keys/lsk_new/revoke': () => {
        revoked = true
        return json(200, { info: key('lsk_new', { status: 'revoked' }) })
      },
    })
    renderApp('/projects/shop/keys')
    await table()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Revoke key lsk_new' }))
    const dialog = screen.getByRole('dialog', { name: 'Revoke this key?' })
    expect(within(dialog).getByText(REVOKE_TEXT)).toBeTruthy()
    expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Cancel' }))
    await user.click(within(dialog).getByRole('button', { name: 'Revoke key' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(bodyRows(screen.getByRole('table'))[0]?.textContent).toContain('revoked'))
  })

  it('shows a refusal plainly and keeps the dialog', async () => {
    setup('admin', { 'POST /api/projects/shop/keys/lsk_new/revoke': () => json(403, {}) })
    renderApp('/projects/shop/keys')
    await table()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Revoke key lsk_new' }))
    await user.click(screen.getByRole('button', { name: 'Revoke key' }))
    expect(await screen.findByText('You do not have permission to do this. Ask a project owner for access.')).toBeTruthy()
    expect(screen.getByRole('dialog', { name: 'Revoke this key?' })).toBeTruthy()
  })
})

describe('configured ingest URL', () => {
  it('uses the runtime config in both examples, with the placeholder', async () => {
    setup('owner', { 'POST /api/projects/shop/keys': () => json(201, { key: SECRET, info: key('lsk_cfg') }) })
    renderApp('/projects/shop/keys', { retries: 0, config: { ingestUrl: 'https://ingest.example.com/base' } })
    await screen.findByRole('table')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Create key' }))
    await user.type(screen.getByLabelText('Name'), 'cfg')
    await user.click(screen.getByRole('button', { name: 'Create' }))
    const reveal = await screen.findByRole('dialog', { name: 'Your new ingest key' })
    const texts = Array.from(reveal.querySelectorAll('pre')).map((p) => p.textContent ?? '')
    expect(texts).toHaveLength(2)
    for (const t of texts) {
      expect(t).toContain('https://ingest.example.com/base/v1/ingest')
      expect(t).not.toContain('localhost:8080')
      expect(t).toContain('YOUR_KEY')
      expect(t).not.toContain(SECRET)
    }
  })

  it('drops a trailing slash from the base', () => {
    expect(curlExample('shop', 0, 'https://ingest.example.com/')).toContain('curl -X POST https://ingest.example.com/v1/ingest ')
    expect(powershellExample('shop', 0, 'https://ingest.example.com//')).toContain('curl.exe -X POST https://ingest.example.com/v1/ingest ')
  })
})
