import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { json, project, renderApp } from '../test/util'
import { mockExplorer } from '../explorer/testing'

// Regression tests for "the palette cannot be closed" (fix/palette-close).
// Root cause: Escape was only handled on the panel element, Ctrl K only
// opened, and the backdrop did nothing, so once focus left the panel (a
// click outside moves it to <body>) nothing closed the palette.

const palettes = () => screen.queryAllByRole('dialog', { name: 'Command palette' })
const palette = () => screen.getByRole('dialog', { name: 'Command palette' })
const combo = () => within(palette()).getByRole('combobox')

function setup() {
  return mockExplorer({ projects: () => json(200, { projects: [project('shop', 'Shop'), project('demo', 'Demo')] }), tail: () => json(503, {}) })
}

/** The keydown/keyup sequence a browser sends for Ctrl K: Control, then k, on whatever has focus. */
function browserCtrlK(target: Element = document.activeElement ?? document.body) {
  const opts = { bubbles: true, cancelable: true, ctrlKey: true }
  const down = new KeyboardEvent('keydown', { ...opts, key: 'Control', code: 'ControlLeft' })
  target.dispatchEvent(down)
  const k = new KeyboardEvent('keydown', { ...opts, key: 'k', code: 'KeyK' })
  target.dispatchEvent(k)
  // keyup goes to whatever has focus now (the palette input after opening).
  ;(document.activeElement ?? document.body).dispatchEvent(new KeyboardEvent('keyup', { ...opts, key: 'k', code: 'KeyK' }))
  ;(document.activeElement ?? document.body).dispatchEvent(new KeyboardEvent('keyup', { bubbles: true, key: 'Control', code: 'ControlLeft' }))
  return k
}
function browserKey(key: string, target: Element = document.activeElement ?? document.body) {
  const ev = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, key })
  target.dispatchEvent(ev)
  target.dispatchEvent(new KeyboardEvent('keyup', { bubbles: true, key }))
  return ev
}

type Opener = 'Ctrl K' | 'Commands button'
async function openWith(how: Opener) {
  renderApp('/projects/shop/logs')
  const search = await screen.findByRole('searchbox')
  const user = userEvent.setup()
  let before: HTMLElement
  if (how === 'Ctrl K') {
    search.focus()
    before = search
    let k = new KeyboardEvent('keydown')
    act(() => {
      k = browserCtrlK(search)
    })
    expect(k.defaultPrevented).toBe(true) // the browser's own Ctrl K does not run
  } else {
    before = screen.getByRole('button', { name: 'Commands' })
    await user.click(before)
  }
  await waitFor(() => expect(palettes()).toHaveLength(1))
  expect(document.activeElement).toBe(combo())
  return { user, before }
}

afterEach(() => vi.unstubAllGlobals())

describe.each<Opener>(['Ctrl K', 'Commands button'])('palette opened with %s', (how) => {
  it('closes with Escape in the input and returns focus', async () => {
    setup()
    const { user, before } = await openWith(how)
    await user.keyboard('{Escape}')
    expect(palettes()).toHaveLength(0)
    expect(document.activeElement).toBe(before)
  })

  it('closes with Escape after arrow keys and with an option as the event target', async () => {
    setup()
    const { user, before } = await openWith(how)
    await user.keyboard('{ArrowDown}{ArrowDown}{ArrowUp}')
    const option = within(palette()).getAllByRole('option')[0] as HTMLElement
    act(() => {
      browserKey('Escape', option)
    })
    expect(palettes()).toHaveLength(0)
    expect(document.activeElement).toBe(before)
  })

  it('closes with Escape after a click outside moved focus to the body (the reported bug)', async () => {
    setup()
    const { before } = await openWith(how)
    const backdrop = palette().parentElement as HTMLElement
    // A mouse press on the backdrop keeps focus in the panel...
    expect(fireEvent.mouseDown(backdrop)).toBe(false)
    // ...but if focus is lost anyway (browser chrome, an extension), Escape on <body> still works.
    act(() => (document.activeElement as HTMLElement).blur())
    expect(document.activeElement).toBe(document.body)
    act(() => {
      browserKey('Escape', document.body)
    })
    expect(palettes()).toHaveLength(0)
    expect(document.activeElement).toBe(before)
  })

  it('closes on a click or tap on the backdrop, not on a click inside the panel', async () => {
    setup()
    const { user, before } = await openWith(how)
    await user.click(combo())
    expect(palettes()).toHaveLength(1)
    await user.click(palette().parentElement as HTMLElement)
    expect(palettes()).toHaveLength(0)
    expect(document.activeElement).toBe(before)
  })

  it('toggles with Ctrl K and Cmd K, from the input and from the body', async () => {
    setup()
    const { before } = await openWith(how)
    act(() => {
      browserCtrlK(combo())
    })
    expect(palettes()).toHaveLength(0)
    expect(document.activeElement).toBe(before)
    act(() => {
      browserCtrlK(document.body)
    })
    expect(palettes()).toHaveLength(1)
    act(() => (document.activeElement as HTMLElement).blur())
    act(() => {
      document.body.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, cancelable: true, key: 'k', metaKey: true }))
    })
    expect(palettes()).toHaveLength(0)
  })

  it('closes with the visible Close button and shows the key hint', async () => {
    setup()
    const { user, before } = await openWith(how)
    expect(within(palette()).getByText('Up and down to move, Enter to run, Esc to close')).toBeTruthy()
    await user.click(within(palette()).getByRole('button', { name: 'Close' }))
    expect(palettes()).toHaveLength(0)
    expect(document.activeElement).toBe(before)
  })

  it('closes on a route change', async () => {
    setup()
    renderApp('/projects/shop/keys')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('link', { name: 'Logs' }))
    await screen.findByRole('treegrid')
    if (how === 'Ctrl K') {
      act(() => {
        browserCtrlK(document.body)
      })
    } else await user.click(screen.getByRole('button', { name: 'Commands' }))
    expect(palettes()).toHaveLength(1)
    act(() => {
      fireEvent.click(screen.getByTestId('history-back')) // like the browser Back button
    })
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/projects/shop/keys'))
    expect(palettes()).toHaveLength(0)
  })

  it('never stacks a second palette, and keeps Tab and Shift+Tab inside', async () => {
    setup()
    const { user } = await openWith(how)
    fireEvent.click(screen.getByRole('button', { name: 'Commands' }))
    expect(palettes()).toHaveLength(1)
    for (const shift of [false, false, false, true, true, true]) {
      await user.tab({ shift })
      expect(palette().contains(document.activeElement)).toBe(true)
    }
  })
})

describe('Escape with the palette open', () => {
  it('does not also reach the explorer (the row selection stays)', async () => {
    setup()
    renderApp('/projects/shop/logs')
    await screen.findByRole('treegrid')
    const user = userEvent.setup()
    await user.keyboard('j')
    const selected = () => document.querySelector('[aria-selected="true"][data-index]')
    expect(selected()).toBeTruthy()
    act(() => {
      browserCtrlK(document.activeElement ?? document.body)
    })
    act(() => {
      browserKey('Escape', document.body)
    })
    expect(palettes()).toHaveLength(0)
    expect(selected()).toBeTruthy()
  })
})
