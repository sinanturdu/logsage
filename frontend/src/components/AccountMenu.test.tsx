import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { alice, json, mockApi, project, renderApp } from '../test/util'
import { LOGOUT_ALL_TEXT, PASSWORD_DONE, passwordProblem } from './AccountMenu'

const where = () => screen.getByTestId('location').textContent ?? ''
type Routes = Parameters<typeof mockApi>[0]
const setup = (extra: Routes = {}) =>
  mockApi({
    'GET /api/auth/me': () => json(200, alice),
    'GET /api/projects': () => json(200, { projects: [project('shop')] }),
    'GET /api/projects/shop/keys': () => json(200, { keys: [] }),
    ...extra,
  })
async function openMenu() {
  renderApp('/projects/shop/keys')
  const user = userEvent.setup()
  const button = await screen.findByRole('button', { name: 'Account' })
  await user.click(button)
  return { user, button }
}
const OLD = 'old-password-123'
const NEW = 'correct horse battery'

afterEach(() => vi.unstubAllGlobals())

describe('account menu', () => {
  it('is a keyboard menu with the email, arrow keys, Escape and focus return', async () => {
    setup()
    const { user, button } = await openMenu()
    expect(button.getAttribute('aria-haspopup')).toBe('menu')
    expect(button.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByText('Signed in as alice@example.com')).toBeTruthy()
    const items = within(screen.getByRole('menu', { name: 'Account' })).getAllByRole('menuitem')
    expect(items.map((i) => i.textContent)).toEqual(['Change password', 'Sign out of all devices', 'Sign out'])
    expect(document.activeElement).toBe(items[0])
    await user.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(items[1])
    await user.keyboard('{ArrowUp}{ArrowUp}')
    expect(document.activeElement).toBe(items[2]) // wraps
    await user.keyboard('{Home}')
    expect(document.activeElement).toBe(items[0])
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('menu')).toBeNull()
    expect(document.activeElement).toBe(button)
    await user.keyboard('{ArrowDown}')
    expect(screen.getByRole('menu')).toBeTruthy()
  })

  it('signs out of all devices after a confirmation', async () => {
    const calls = setup({ 'POST /api/auth/logout-all': () => json(204) })
    const { user } = await openMenu()
    await user.click(screen.getByRole('menuitem', { name: 'Sign out of all devices' }))
    const dialog = screen.getByRole('dialog', { name: 'Sign out of all devices?' })
    expect(within(dialog).getByText(LOGOUT_ALL_TEXT)).toBeTruthy()
    expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Cancel' }))
    await user.click(within(dialog).getByRole('button', { name: 'Sign out everywhere' }))
    await waitFor(() => expect(where()).toBe('/login'))
    expect(calls.some((c) => c.key === 'POST /api/auth/logout-all')).toBe(true)
  })

  it('cancels signing out of all devices and returns focus', async () => {
    const calls = setup()
    const { user, button } = await openMenu()
    await user.click(screen.getByRole('menuitem', { name: 'Sign out of all devices' }))
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(button)
    expect(calls.some((c) => c.key === 'POST /api/auth/logout-all')).toBe(false)
  })
})

describe('change password', () => {
  it('validates length and confirmation before sending', () => {
    expect(passwordProblem('short', 'short')).toBe('The new password needs at least 12 characters.')
    expect(passwordProblem('x'.repeat(129), 'x'.repeat(129))).toBe('The new password can have at most 128 characters.')
    expect(passwordProblem('ü'.repeat(12), 'ü'.repeat(12))).toBe('') // characters, not bytes
    expect(passwordProblem(NEW, `${NEW}!`)).toBe('The two new passwords do not match. Type the same password twice.')
  })

  it('changes the password and says other sessions were signed out', async () => {
    let body: unknown
    const log = vi.spyOn(console, 'log')
    setup({
      'POST /api/auth/change-password': (init) => {
        body = JSON.parse(String(init?.body))
        return json(204)
      },
    })
    const { user } = await openMenu()
    await user.click(screen.getByRole('menuitem', { name: 'Change password' }))
    const dialog = screen.getByRole('dialog', { name: 'Change password' })
    expect(within(dialog).getByText('Changing it signs out your other sessions.')).toBeTruthy()
    await user.type(within(dialog).getByLabelText('Current password'), OLD)
    await user.type(within(dialog).getByLabelText('New password'), NEW)
    await user.type(within(dialog).getByLabelText('New password again'), 'different password!')
    await user.click(within(dialog).getByRole('button', { name: 'Change password' }))
    expect(within(dialog).getByRole('alert').textContent).toBe('The two new passwords do not match. Type the same password twice.')
    expect(body).toBeUndefined()
    await user.clear(within(dialog).getByLabelText('New password again'))
    await user.type(within(dialog).getByLabelText('New password again'), NEW)
    await user.click(within(dialog).getByRole('button', { name: 'Change password' }))
    expect(await screen.findByText(PASSWORD_DONE)).toBeTruthy()
    expect(body).toEqual({ current_password: OLD, new_password: NEW })
    expect(JSON.stringify(log.mock.calls)).not.toContain(NEW)
    expect(JSON.stringify({ ...localStorage, ...sessionStorage })).not.toContain(NEW)
    await user.click(screen.getByRole('button', { name: 'Close' }))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it.each([
    [403, {}, 'The current password is not correct. Try again.'],
    [400, { error: 'password must be at least 12 characters' }, 'Password must be at least 12 characters.'],
    [429, {}, 'Too many requests. Try again in 30 seconds.'],
  ])('shows a %i plainly', async (status, payload, text) => {
    setup({ 'POST /api/auth/change-password': () => json(status, payload, { 'Retry-After': '30' }) })
    const { user } = await openMenu()
    await user.click(screen.getByRole('menuitem', { name: 'Change password' }))
    const dialog = screen.getByRole('dialog', { name: 'Change password' })
    await user.type(within(dialog).getByLabelText('Current password'), OLD)
    await user.type(within(dialog).getByLabelText('New password'), NEW)
    await user.type(within(dialog).getByLabelText('New password again'), NEW)
    await user.click(within(dialog).getByRole('button', { name: 'Change password' }))
    expect(await within(dialog).findByText(text)).toBeTruthy()
  })
})
