import { screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import appCss from '../styles/app.css?raw'
import { json, renderApp } from '../test/util'
import { group, mockGroups, page } from './testing'

afterEach(() => vi.unstubAllGlobals())

// jsdom has no layout, so the alignment rule is checked in the stylesheet:
// the explorer drops the main's padding and pads its sections; pages inside
// the padded main must not pad their head again.
describe('left edge of the groups pages', () => {
  it('the page head has no side padding, after every explorer-head rule', () => {
    const rule = appCss.lastIndexOf('.page-head { padding-left: 0; padding-right: 0; }')
    expect(rule).toBeGreaterThan(-1)
    expect(rule).toBeGreaterThan(appCss.lastIndexOf('.explorer-head'))
    expect(appCss).toContain('.shell-main:has(.explorer) { padding: 0; }')
  })

  it.each([
    ['/projects/shop/groups', 'GET /api/projects/shop/groups', () => page([group('aaaaaaaaaaaaaaaa')])],
    ['/projects/shop/groups/0123456789abcdef', 'GET /api/projects/shop/groups/0123456789abcdef', () => json(200, { ...group('0123456789abcdef'), counts: [], samples: [], note: '', updated_by: '' })],
  ])('%s uses the page head inside the padded main', async (path, route, answer) => {
    mockGroups({ [route]: answer })
    renderApp(path)
    await screen.findAllByRole('group', { name: 'Time range' })
    const heads = document.querySelectorAll('.explorer-head')
    expect(heads.length).toBe(1)
    expect(heads[0]?.classList.contains('page-head')).toBe(true)
    expect(document.querySelector('.explorer')).toBeNull() // the main keeps its padding
  })
})
