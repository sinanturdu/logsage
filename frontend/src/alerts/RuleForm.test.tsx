import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { json, renderApp } from '../test/util'
import { PREVIEW_ANNOUNCE_MS } from './RuleForm'
import { channel, mockAlerts, rule } from './testing'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const B = '/api/projects/shop/alerts'
const CHANNELS = [channel('c1', { name: 'Slack ops', kind: 'slack' }), channel('c2', { name: 'pager' })]

async function openForm(routes: Parameters<typeof mockAlerts>[0] = {}) {
  const m = mockAlerts({ [`GET ${B}/channels`]: () => json(200, { channels: CHANNELS }), ...routes })
  renderApp('/projects/shop/alerts')
  const user = userEvent.setup()
  await user.click(await screen.findByRole('button', { name: 'Create rule' }))
  const dialog = await screen.findByRole('dialog', { name: 'Create an alert rule' })
  return { ...m, user, dialog }
}
const labels = (d: HTMLElement) =>
  Array.from(d.querySelectorAll('.field-row label, fieldset legend'))
    .map((l) => l.textContent)
    .filter((t) => t !== 'Type' && t !== 'Notify')

describe('rule form', () => {
  it('shows only the fields of the chosen type', async () => {
    const { user, dialog } = await openForm()
    expect(document.activeElement).toBe(within(dialog).getByLabelText('Name'))
    expect(labels(dialog)).toEqual([
      'Threshold (%)',
      'Minimum logs',
      'Window',
      'Services (none chosen: all services)',
      'For',
      'Clear for',
      'Check every',
    ])
    await user.click(within(dialog).getByRole('radio', { name: /Log volume/ }))
    expect(labels(dialog)).toEqual(['Direction', 'Count', 'Window', 'Levels (none chosen: all levels)', 'Services (none chosen: all services)', 'For', 'Clear for', 'Check every'])
    await user.click(within(dialog).getByRole('radio', { name: /New error group/ }))
    expect(labels(dialog)).toEqual(['Minimum events', 'Window', 'Services (none chosen: all services)', 'For', 'Clear for', 'Check every'])
    await user.click(within(dialog).getByRole('radio', { name: /^Regression/ }))
    expect(labels(dialog)).toEqual(['Window', 'Services (none chosen: all services)', 'For', 'Clear for', 'Check every'])
    await user.click(within(dialog).getByRole('radio', { name: /New groups per hour/ }))
    expect(labels(dialog)).toEqual(['Max new groups', 'For', 'Clear for', 'Check every'])
  })

  it('gives every field in a row a short label, then the input, then its help text', async () => {
    const { user, dialog } = await openForm()
    for (const t of [/Error rate/, /Log volume/, /New error group/, /New groups per hour/]) {
      await user.click(within(dialog).getByRole('radio', { name: t }))
      for (const field of dialog.querySelectorAll('.field-row > .afield')) {
        const [label, control, hint] = Array.from(field.children)
        expect(label?.tagName).toBe('LABEL')
        expect(label?.textContent?.length).toBeLessThanOrEqual(16)
        const input = control?.matches('input, select') ? control : control?.querySelector('input')
        expect(input?.id).toBe(label?.getAttribute('for'))
        expect(hint?.className).toBe('field-hint')
        expect(input?.getAttribute('aria-describedby')?.split(' ')).toContain(hint?.id)
      }
    }
  })

  it('is a header, a scrolling body and a footer with the preview and the buttons', async () => {
    const { dialog } = await openForm()
    expect(Array.from(dialog.children).map((c) => c.tagName)).toEqual(['H2', 'FORM'])
    const form = dialog.querySelector('form') as HTMLFormElement
    expect(Array.from(form.children).map((c) => c.className)).toEqual(['dialog-body', 'dialog-footer'])
    const body = form.children[0] as HTMLElement
    const footer = form.children[1] as HTMLElement
    expect(within(body).getByLabelText('Name')).toBeTruthy()
    expect(within(footer).getByTestId('rule-preview').textContent).toContain('Fires when')
    expect(within(footer).getByRole('button', { name: 'Cancel' })).toBeTruthy()
    expect(within(footer).getByRole('button', { name: 'Create rule' })).toBeTruthy()
    const toggle = within(footer).getByRole('button', { name: 'Show preview' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(toggle.getAttribute('aria-controls')).toBe(within(footer).getByTestId('rule-preview').id)
    await userEvent.setup().click(toggle)
    expect(within(footer).getByRole('button', { name: 'Hide preview' }).getAttribute('aria-expanded')).toBe('true')
    expect(within(footer).getByTestId('rule-preview').className).toBe('preview open')
  })

  it('announces the preview politely once typing pauses', async () => {
    const { dialog } = await openForm()
    const live = within(dialog).getByTestId('preview-announcement')
    expect(live.getAttribute('aria-live')).toBe('polite')
    await waitFor(() => expect(live.textContent).toContain('at least 5% of all logs'))
    // From here only the fake clock moves time, so the pause is exact.
    vi.useFakeTimers()
    try {
      const threshold = within(dialog).getByLabelText('Threshold (%)')
      fireEvent.change(threshold, { target: { value: '3' } })
      fireEvent.change(threshold, { target: { value: '37' } })
      // The visible preview follows at once; the announcement waits for the pause.
      expect(within(dialog).getByTestId('rule-preview').textContent).toContain('at least 37%')
      expect(live.textContent).not.toContain('37%')
      await act(() => vi.advanceTimersByTimeAsync(PREVIEW_ANNOUNCE_MS - 1))
      expect(live.textContent).not.toContain('37%')
      await act(() => vi.advanceTimersByTimeAsync(1))
      expect(live.textContent).toContain('at least 37% of all logs')
      expect(live.textContent).toContain('Resolves when')
    } finally {
      vi.useRealTimers()
    }
  })

  it('updates the preview sentence as the user types', async () => {
    const { user, dialog } = await openForm()
    const preview = within(dialog).getByTestId('rule-preview')
    const threshold = within(dialog).getByLabelText('Threshold (%)')
    await user.clear(threshold)
    await user.type(threshold, '20')
    await user.clear(within(dialog).getByLabelText('For'))
    await user.type(within(dialog).getByLabelText('For'), '2')
    await user.click(within(dialog).getByRole('checkbox', { name: /Slack ops/ }))
    expect(preview.textContent).toContain(
      'Fires when error and fatal logs are at least 20% of all logs over 5 minutes, with at least 50 logs, for at least 2 minutes. Notifies: Slack ops.',
    )
    expect(preview.textContent).toContain('Resolves when the share falls below 18%')
    await user.click(await within(dialog).findByRole('checkbox', { name: 'payments' }))
    expect(preview.textContent).toContain('of all logs in service payments over 5 minutes')
  })

  it('validates with the server limits, next to the fields, and focuses the first', async () => {
    const { user, dialog, requests } = await openForm()
    const threshold = within(dialog).getByLabelText('Threshold (%)')
    await user.clear(threshold)
    await user.type(threshold, '0.01')
    await user.clear(within(dialog).getByLabelText('Window'))
    await user.type(within(dialog).getByLabelText('Window'), '0')
    await user.click(within(dialog).getByLabelText('Note (optional)'))
    await user.paste('x'.repeat(501))
    await user.click(within(dialog).getByRole('checkbox', { name: /pager/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Create rule' }))
    const name = within(dialog).getByLabelText('Name')
    expect(document.activeElement).toBe(name)
    expect(name.getAttribute('aria-invalid')).toBe('true')
    expect(name.getAttribute('aria-describedby')).toContain('error')
    expect(dialog.textContent).toContain('Enter a name.')
    expect(dialog.textContent).toContain('Enter a percentage between 0.1 and 100.')
    expect(dialog.textContent).toContain('Enter a duration between 1 minute and 1 day.')
    expect(dialog.textContent).toContain('Use at most 500 characters.')
    expect(requests.some((r) => r.method === 'POST')).toBe(false)
  })

  it('keeps Create disabled with the reason until a channel is chosen', async () => {
    const { user, dialog } = await openForm()
    const create = within(dialog).getByRole('button', { name: 'Create rule' })
    expect(create.hasAttribute('disabled')).toBe(true)
    expect(document.getElementById(create.getAttribute('aria-describedby') ?? '')?.textContent).toBe('Choose at least one channel.')
    await user.click(within(dialog).getByRole('checkbox', { name: /pager/ }))
    expect(create.hasAttribute('disabled')).toBe(false)
    expect(create.getAttribute('aria-describedby')).toBeNull()
    expect(within(dialog).queryByText('Choose at least one channel.')).toBeNull()
  })

  it('creates a rule with only the chosen type parameters', async () => {
    const { user, dialog, requests } = await openForm({ [`POST ${B}/rules`]: () => json(201, { rule: rule('r9') }) })
    await user.type(within(dialog).getByLabelText('Name'), 'Silence')
    await user.click(within(dialog).getByRole('radio', { name: /Log volume/ }))
    await user.selectOptions(within(dialog).getByLabelText('Direction'), 'below')
    await user.clear(within(dialog).getByLabelText('Count'))
    await user.type(within(dialog).getByLabelText('Count'), '1')
    await user.click(within(dialog).getByRole('checkbox', { name: 'error' }))
    await user.click(within(dialog).getByRole('checkbox', { name: /pager/ }))
    await user.click(within(dialog).getByRole('checkbox', { name: 'Notify again while firing' }))
    await user.click(within(dialog).getByRole('button', { name: 'Create rule' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    const post = requests.find((r) => r.method === 'POST')
    expect(post?.body).toEqual({
      name: 'Silence',
      note: '',
      type: 'log_volume',
      params: { op: 'below', count: 1, levels: ['error'] },
      interval: '60s',
      window: '300s',
      for: '0s',
      clear_for: '60s',
      repeat: '3600s',
      enabled: true,
      channels: ['c2'],
    })
  })

  it('shows a server field error next to its field', async () => {
    const { user, dialog } = await openForm({ [`POST ${B}/rules`]: () => json(400, { error: 'window: must be between 1m and 24h, in whole seconds' }) })
    await user.type(within(dialog).getByLabelText('Name'), 'x')
    await user.click(within(dialog).getByRole('checkbox', { name: /pager/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Create rule' }))
    const win = within(dialog).getByLabelText('Window')
    await waitFor(() => expect(win.getAttribute('aria-invalid')).toBe('true'))
    expect(document.getElementById(`${win.id}-error`)?.textContent).toBe('Must be between 1m and 24h, in whole seconds.')
    expect(document.activeElement).toBe(win)
  })

  it('shows a limit or other server error for the whole form', async () => {
    const { user, dialog } = await openForm({ [`POST ${B}/rules`]: () => json(409, { error: 'rules: at most 50 per project' }) })
    await user.type(within(dialog).getByLabelText('Name'), 'x')
    await user.click(within(dialog).getByRole('checkbox', { name: /pager/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Create rule' }))
    expect((await within(dialog).findByRole('alert')).textContent).toBe('Rules: at most 50 per project.')
  })

  it('edits a rule with its values filled in and PUTs it', async () => {
    const r = rule('r1', { name: 'Errors', params: { threshold_percent: 12.5, min_logs: 80 }, channels: ['c1'] })
    const m = mockAlerts({
      [`GET ${B}/rules`]: () => json(200, { rules: [r] }),
      [`GET ${B}/channels`]: () => json(200, { channels: CHANNELS }),
      [`PUT ${B}/rules/r1`]: () => json(200, { rule: r }),
    })
    renderApp('/projects/shop/alerts')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Edit rule Errors' }))
    const dialog = await screen.findByRole('dialog', { name: 'Edit alert rule' })
    expect((within(dialog).getByLabelText('Threshold (%)') as HTMLInputElement).value).toBe('12.5')
    expect((within(dialog).getByRole('checkbox', { name: /Slack ops/ }) as HTMLInputElement).checked).toBe(true)
    await user.click(within(dialog).getByRole('button', { name: 'Save rule' }))
    await waitFor(() => expect(m.requests.find((x) => x.method === 'PUT')?.body).toMatchObject({ params: { threshold_percent: 12.5, min_logs: 80 }, for: '120s' }))
  })

  it('traps focus and closes with Escape', async () => {
    const { user, dialog } = await openForm()
    const create = within(dialog).getByRole('button', { name: 'Create rule' })
    create.focus()
    await user.tab()
    expect(dialog.contains(document.activeElement)).toBe(true)
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement?.textContent).toBe('Create rule')
  })
})
