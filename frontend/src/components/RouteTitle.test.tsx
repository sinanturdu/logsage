import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { alice, json, mockApi, project, renderApp } from '../test/util'
import { titleFor } from './RouteTitle'

afterEach(() => vi.unstubAllGlobals())

describe('document titles', () => {
  it.each([
    ['/login', 'Sign in - LogSage'],
    ['/', 'Projects - LogSage'],
    ['/projects/demo/logs', 'Logs - demo - LogSage'],
    ['/projects/demo/keys', 'API keys - demo - LogSage'],
    ['/projects/a%20b/keys', 'API keys - a b - LogSage'],
    ['/projects/%E0%A4%A/logs', 'Logs - %E0%A4%A - LogSage'],
    ['/nope', 'Page not found - LogSage'],
  ])('%s is %s', (path, title) => {
    expect(titleFor(path)).toBe(title)
  })

  it('updates on navigation and announces the new page politely, not on filter changes', async () => {
    mockApi({
      'GET /api/auth/me': () => json(200, alice),
      'GET /api/projects': () => json(200, { projects: [project('demo')] }),
      'GET /api/projects/demo/keys': () => json(200, { keys: [] }),
    })
    renderApp('/projects/demo/keys')
    await waitFor(() => expect(document.title).toBe('API keys - demo - LogSage'))
    const announcer = screen.getByTestId('route-announcer')
    expect(announcer.getAttribute('aria-live')).toBe('polite')
    expect(announcer.textContent).toBe('') // the first page is not announced
    await userEvent.setup().click(await screen.findByRole('link', { name: 'Logs' }))
    await waitFor(() => expect(document.title).toBe('Logs - demo - LogSage'))
    await waitFor(() => expect(announcer.textContent).toBe('Logs - demo - LogSage'))
  })

  it('renders an HTML payload in a slug as text', () => {
    expect(titleFor('/projects/%3Cimg%20src%3Dx%20onerror%3Dalert(1)%3E/logs')).toBe('Logs - <img src=x onerror=alert(1)> - LogSage')
  })
})
