import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { json, renderApp } from '../test/util'
import { scopeText } from './ExplorerPage'
import { segments } from './Timeline'
import { facets, mockExplorer, type Req } from './testing'

const SEL = 'from=2026-11-07T02%3A00%3A00Z&to=2026-11-07T02%3A15%3A00Z'
const where = () => screen.getByTestId('location').textContent ?? ''
const back = () => fireEvent.click(screen.getByTestId('history-back'))
const lastHistogram = (rs: Req[]) => rs.filter((r) => r.path.endsWith('/histogram')).at(-1) as Req

describe('zoom', () => {
  it('zooms the timeline to the selection, zooms out, and follows Back', async () => {
    const { requests } = mockExplorer()
    renderApp(`/projects/shop/logs?${SEL}`)
    const user = userEvent.setup()
    await screen.findByRole('treegrid', { name: 'Logs, newest first' })
    const wide = lastHistogram(requests)
    expect(Date.parse(wide.params.get('to') ?? '') - Date.parse(wide.params.get('from') ?? '')).toBeGreaterThan(15 * 60_000)

    await user.click(await screen.findByRole('button', { name: 'Zoom to selection' }))
    await waitFor(() => expect(where()).toBe(`/projects/shop/logs?${SEL}&zoom=1`))
    await waitFor(() => expect(lastHistogram(requests).params.get('from')).toBe('2026-11-07T02:00:00Z'))
    expect(lastHistogram(requests).params.get('to')).toBe('2026-11-07T02:15:00Z')
    expect(screen.getByRole('heading', { level: 1 }).textContent).toContain('zoomed to the selection')

    await user.click(screen.getByRole('button', { name: 'Zoom out' }))
    await waitFor(() => expect(where()).toBe(`/projects/shop/logs?${SEL}`))
    expect(await screen.findByRole('button', { name: 'Zoom to selection' })).toBeTruthy()

    back()
    await waitFor(() => expect(where()).toBe(`/projects/shop/logs?${SEL}&zoom=1`))
    back()
    await waitFor(() => expect(where()).toBe(`/projects/shop/logs?${SEL}`))
  })

  it('offers no zoom without a selection, and ignores zoom=1 on a relative range', async () => {
    mockExplorer()
    renderApp('/projects/shop/logs?range=1h&zoom=1')
    await screen.findByRole('treegrid', { name: 'Logs, newest first' })
    expect(screen.queryByRole('button', { name: /Zoom/ })).toBeNull()
  })
})

describe('scope labels', () => {
  it('words the counts plainly', () => {
    expect(scopeText(4226, 2113)).toBe('4,226 logs in view, 2,113 in the selection.')
    expect(scopeText(1)).toBe('1 log in view.')
  })

  it('shows the view and selection counts and keeps Showing N of TOTAL', async () => {
    mockExplorer({ facets: () => json(200, { ...facets(3), total: 2113 }) })
    renderApp(`/projects/shop/logs?${SEL}`)
    expect(await screen.findByText('53 logs in view, 2,113 in the selection.')).toBeTruthy()
    expect(await screen.findByText('Showing 3 of 2,113 logs, newest first.')).toBeTruthy()
  })
})

describe('small samples in the error share', () => {
  it('breaks the line at buckets with fewer than 5 logs', () => {
    expect(segments([10, 20, null, 30], 100)).toHaveLength(2)
    expect(segments([null, null], 100)).toEqual([])
    const [lone] = segments([null, 50, null], 100)
    expect(lone?.split(' ')).toHaveLength(2) // a lone point is drawn as a short stroke
  })

  it('draws no share for small buckets and says why', async () => {
    mockExplorer({
      histogram: () =>
        json(200, {
          bucket_seconds: 10,
          buckets: [
            { t: '2026-11-07T02:16:20Z', debug: 0, info: 10, warn: 0, error: 0, fatal: 0 },
            { t: '2026-11-07T02:16:30Z', debug: 0, info: 1, warn: 0, error: 2, fatal: 0 },
            { t: '2026-11-07T02:16:40Z', debug: 0, info: 8, warn: 0, error: 2, fatal: 0 },
          ],
          took_ms: 1,
        }),
    })
    renderApp('/projects/shop/logs')
    const chart = await screen.findByRole('region', { name: 'Timeline' })
    await waitFor(() => expect(chart.querySelectorAll('polyline.err')).toHaveLength(2))
    const about = chart.querySelector('details') as HTMLDetailsElement
    expect(about.querySelector('summary')?.textContent).toBe('About this chart')
    expect(about.textContent).toContain('Buckets with fewer than 5 logs are left out of the error share (this view has some)')
    expect(about.textContent).toContain('The upper strip counts logs per 10 s.')
    expect(about.textContent).toContain('Drag across either strip to select a time range')
    expect(about.open).toBe(false) // closed until asked for
    const key = chart.querySelector('.key')?.textContent ?? ''
    expect(key).toMatch(/^Peak volume /) // the total lives in the scope line only
    expect(key).toContain('Drag across either strip to select a time range.')
    expect(key).not.toContain('fewer than 5')
    // The 3-log bucket is 67% errors; the peak reported is the 10-log bucket's 20%.
    expect(within(chart).getByText('peak 20%')).toBeTruthy()
    expect(within(chart).getByRole('img').getAttribute('aria-label')).not.toContain('67%')
  })
})

describe('error share with only error and fatal selected', () => {
  it('knows when the share would be 100% by definition', async () => {
    const { onlyErrorLevels, summarize } = await import('./time')
    expect(onlyErrorLevels(['error', 'fatal'])).toBe(true)
    expect(onlyErrorLevels(['error'])).toBe(true)
    expect(onlyErrorLevels(['fatal'])).toBe(true)
    expect(onlyErrorLevels([])).toBe(false) // all levels
    expect(onlyErrorLevels(['warn', 'error'])).toBe(false)
    const buckets = [{ t: Date.UTC(2026, 10, 7, 2, 16, 30), debug: 0, info: 0, warn: 0, error: 30, fatal: 1 }]
    expect(summarize(buckets, 10, { share: false })).toBe('Peak volume 31 logs per 10 s at 02:16:30 UTC.')
    expect(summarize(buckets, 10)).toContain('Peak error and fatal share 100%')
  })

  it.each([
    ['level=error%2Cfatal', false],
    ['level=error', false],
    ['level=fatal', false],
    ['level=warn%2Cerror', true],
    ['', true],
  ])('%s: strip shown = %s', async (query, shown) => {
    mockExplorer()
    renderApp(`/projects/shop/logs${query ? `?${query}` : ''}`)
    const chart = await screen.findByRole('region', { name: 'Timeline' })
    await waitFor(() => expect(chart.querySelectorAll('polyline.vol').length).toBeGreaterThan(0))
    const hidden = within(chart).queryByText('Error share is not shown while only error and fatal levels are selected.')
    expect(chart.querySelectorAll('polyline.err').length > 0).toBe(shown)
    expect(hidden === null).toBe(shown)
    const key = chart.querySelector('.key')?.textContent ?? ''
    expect(key.includes('error and fatal share') || key.includes('No errors') || key.includes('enough logs')).toBe(shown)
  })
})
