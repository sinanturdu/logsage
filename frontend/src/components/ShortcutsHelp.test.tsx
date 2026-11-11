import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { alice, json, mockApi, project, renderApp } from '../test/util'
import { SHORTCUTS, shortcutFor } from '../shortcuts'

afterEach(() => vi.unstubAllGlobals())
const setup = () =>
  mockApi({
    'GET /api/auth/me': () => json(200, alice),
    'GET /api/projects': () => json(200, { projects: [project('demo')] }),
    'GET /api/projects/demo/keys': () => json(200, { keys: [] }),
  })

describe('keyboard shortcuts help', () => {
  it('opens with ?, lists exactly the shared definition, and closes with Escape', async () => {
    setup()
    renderApp('/projects/demo/keys')
    await screen.findByRole('heading', { name: 'API keys' })
    const user = userEvent.setup()
    await user.keyboard('?')
    const dialog = screen.getByRole('dialog', { name: 'Keyboard shortcuts' })
    const listed = Array.from(dialog.querySelectorAll<HTMLElement>('[data-shortcut]'))
    expect(listed.map((d) => d.dataset.shortcut)).toEqual(['palette', 'help', 'search', 'next', 'previous', 'toggle', 'escape', 'trace', 'live', 'retry', 'groups-search', 'groups-next', 'groups-previous', 'groups-open', 'alerts-next', 'alerts-previous', 'alerts-open', 'incidents-search', 'incidents-next', 'incidents-previous', 'incidents-open'])
    for (const s of SHORTCUTS) {
      const row = dialog.querySelector(`[data-shortcut="${s.id}"]`) as HTMLElement
      expect(Array.from(row.querySelectorAll('kbd')).map((k) => k.textContent)).toEqual(s.keys)
      expect(within(row).getByText(s.description)).toBeTruthy()
    }
    expect(Array.from(dialog.querySelectorAll('kbd')).map((k) => k.textContent)).toEqual(
      expect.arrayContaining(['/', 'j', 'k', 'Enter', 'Space', 'Escape', 't', 'l', 'r', 'Ctrl K', '?']),
    )
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('ignores ? while typing in a field', async () => {
    setup()
    renderApp('/projects/demo/logs')
    const user = userEvent.setup()
    await user.type(await screen.findByRole('searchbox'), '?')
    expect(screen.queryByRole('dialog', { name: 'Keyboard shortcuts' })).toBeNull()
  })

  it('matches keys only as defined', () => {
    const t = (key: string, mods: Partial<KeyboardEvent> = {}, target: EventTarget | null = document.body) =>
      shortcutFor({ key, ctrlKey: false, metaKey: false, altKey: false, ...mods, target }, SHORTCUTS.map((s) => s.id))
    expect(t('k')).toBe('previous')
    expect(t('k', { ctrlKey: true })).toBe('palette')
    expect(t('K', { metaKey: true })).toBe('palette')
    expect(t('k', { ctrlKey: true }, document.createElement('input'))).toBe('palette') // works while typing
    expect(t('j', {}, document.createElement('input'))).toBeNull()
    expect(t(' ')).toBe('toggle')
    expect(t('x')).toBeNull()
  })
})
