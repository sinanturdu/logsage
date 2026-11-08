import { screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { alice, json, mockApi, project, renderApp } from '../test/util'
import { parseFingerprint } from './model'

afterEach(() => vi.unstubAllGlobals())

describe('parseFingerprint', () => {
  it.each([
    ['0123456789abcdef', '0123456789abcdef'],
    ['0123456789ABCDEF', '0123456789abcdef'],
    ['0123456789abcde', null],
    ['0123456789abcdef0', null],
    ['0123456789abcdeg', null],
    ['0000000000000000', null],
    ['', null],
    [null, null],
    ['<script>alert(1)</script>', null],
  ])('%s gives %s', (input, want) => {
    expect(parseFingerprint(input)).toBe(want)
  })
})

describe('group routes', () => {
  const setup = () =>
    mockApi({
      'GET /api/auth/me': () => json(200, alice),
      'GET /api/projects': () => json(200, { projects: [project('shop')] }),
    })

  it('header nav lists Logs, Error groups and API keys', async () => {
    setup()
    renderApp('/projects/shop/keys')
    const nav = await screen.findByRole('navigation', { name: 'Project sections' })
    expect([...nav.querySelectorAll('a')].map((a) => [a.textContent, a.getAttribute('href')])).toEqual([
      ['Logs', '/projects/shop/logs'],
      ['Error groups', '/projects/shop/groups'],
      ['API keys', '/projects/shop/keys'],
    ])
  })

  it.each(['nothex', '0123456789abcdeZ', '0000000000000000', '0123456789abcdef00'])('%s is a clear 404 page', async (fp) => {
    setup()
    renderApp(`/projects/shop/groups/${fp}`)
    await screen.findByRole('heading', { name: 'Group not found' })
    screen.getByText(/not an error group: a group is named by 16 hexadecimal characters/)
    expect(screen.getByRole('link', { name: 'Go to Error groups' }).getAttribute('href')).toBe('/projects/shop/groups')
  })
})
