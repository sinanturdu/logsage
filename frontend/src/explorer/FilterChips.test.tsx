import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { renderApp } from '../test/util'
import { mockExplorer } from './testing'

const TRACE = '4bf92f3577b34da6a3ce929d0e0e4736'
const where = () => screen.getByTestId('location').textContent ?? ''
const chips = () => screen.queryByRole('group', { name: 'Active filters' })
const names = () =>
  within(chips() as HTMLElement)
    .getAllByRole('button')
    .map((b) => b.getAttribute('aria-label') ?? b.textContent)

describe('filter chips', () => {
  it('shows no chips without filters', async () => {
    mockExplorer()
    renderApp('/projects/shop/logs')
    await screen.findByRole('treegrid', { name: 'Logs, newest first' })
    expect(chips()).toBeNull()
  })

  it('lists one chip per active filter with readable labels', async () => {
    mockExplorer()
    renderApp(`/projects/shop/logs?from=2026-11-07T02:00:00Z&to=2026-11-07T02:15:00Z&level=error,fatal&service=api,checkout&q=timeout&trace_id=${TRACE}`)
    await screen.findByRole('group', { name: 'Active filters' })
    expect(names()).toEqual([
      'Remove filter range 02:00:00 to 02:15:00 UTC',
      'Remove filter level error, fatal',
      'Remove filter service api',
      'Remove filter service checkout',
      'Remove filter search timeout',
      `Remove filter trace ${TRACE}`,
      'Clear filters',
    ])
  })

  it('adds a chip when a filter is set and removes one filter per chip', async () => {
    mockExplorer()
    renderApp('/projects/shop/logs?service=api,checkout&q=timeout')
    const user = userEvent.setup()
    await screen.findByRole('group', { name: 'Active filters' })
    await user.click(screen.getByRole('button', { name: 'Remove filter service api' }))
    await waitFor(() => expect(where()).toBe('/projects/shop/logs?service=checkout&q=timeout'))
    await user.click(screen.getByRole('button', { name: 'Remove filter search timeout' }))
    await waitFor(() => expect(where()).toBe('/projects/shop/logs?service=checkout'))
    await user.click(screen.getByRole('checkbox', { name: /^info/ }))
    await waitFor(() => expect(names().some((n) => n?.startsWith('Remove filter level '))).toBe(true))
  })

  it('clears all filters but keeps the range, which has its own chip', async () => {
    mockExplorer()
    renderApp('/projects/shop/logs?range=1h&level=warn&service=api')
    const user = userEvent.setup()
    await screen.findByRole('group', { name: 'Active filters' })
    await user.click(within(chips() as HTMLElement).getByRole('button', { name: 'Clear filters' }))
    await waitFor(() => expect(where()).toBe('/projects/shop/logs?range=1h'))
    expect(chips()).toBeNull()
  })

  it('removing the range chip goes back to the last 15 minutes and leaves zoom', async () => {
    mockExplorer()
    renderApp('/projects/shop/logs?from=2026-11-07T02:00:00Z&to=2026-11-07T02:15:00Z&zoom=1&service=api')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /^Remove filter range/ }))
    await waitFor(() => expect(where()).toBe('/projects/shop/logs?service=api'))
  })

  it('renders an HTML payload in a chip as text', async () => {
    mockExplorer()
    renderApp(`/projects/shop/logs?q=${encodeURIComponent('<img src=x onerror=alert(1)>')}`)
    const group = await screen.findByRole('group', { name: 'Active filters' })
    expect(group.querySelector('img')).toBeNull()
    expect(group.textContent).toContain('search <img src=x onerror=alert(1)>')
  })
})

describe('Filters disclosure', () => {
  it('counts active filters and toggles the panel; search and chips stay outside it', async () => {
    mockExplorer()
    renderApp('/projects/shop/logs?level=error&service=api&q=timeout')
    const user = userEvent.setup()
    const toggle = await screen.findByRole('button', { name: 'Filters (3)' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    const panel = document.getElementById(toggle.getAttribute('aria-controls') ?? '') as HTMLElement
    expect(panel.dataset.open).toBe('false')
    expect(panel.contains(screen.getByRole('searchbox'))).toBe(false)
    expect(panel.contains(chips())).toBe(false)
    await user.click(toggle)
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(panel.dataset.open).toBe('true')
    await user.click(screen.getByRole('button', { name: 'Remove filter service api' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Filters (2)' }).getAttribute('aria-expanded')).toBe('true'))
    expect(where()).not.toContain('open') // state lives in the component, not the URL
  })
})
