import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { alice, json, mockApi, project, renderApp } from '../test/util'

const me = () => json(200, alice)
const where = () => screen.getByTestId('location').textContent

describe('project switcher', () => {
  it('shows loading while projects load', async () => {
    mockApi({ 'GET /api/auth/me': me, 'GET /api/projects': () => new Promise<Response>(() => {}) })
    renderApp('/projects/shop/logs')
    const header = await screen.findByRole('banner')
    expect(await within(header).findByText('Loading projects…')).toBeTruthy()
  })

  it('lists projects and switches between them', async () => {
    mockApi({ 'GET /api/auth/me': me, 'GET /api/projects': () => json(200, { projects: [project('shop', 'Shop'), project('billing', 'Billing')] }) })
    renderApp('/projects/shop/logs')
    const select = (await screen.findByLabelText('Project')) as HTMLSelectElement
    expect(select.value).toBe('shop')
    expect(within(select).getAllByRole('option').map((o) => o.textContent)).toEqual(['shop', 'billing'])
    await userEvent.setup().selectOptions(select, 'billing')
    await waitFor(() => expect(where()).toBe('/projects/billing/logs'))
  })

  it('opens the first project from the start page', async () => {
    mockApi({ 'GET /api/auth/me': me, 'GET /api/projects': () => json(200, { projects: [project('shop', 'Shop')] }) })
    renderApp('/')
    await waitFor(() => expect(where()).toBe('/projects/shop/logs'))
  })

  it('shows an error with retry', async () => {
    let fail = true
    mockApi({
      'GET /api/auth/me': me,
      'GET /api/projects': () => (fail ? json(503, { error: 'service unavailable' }) : json(200, { projects: [project('shop')] })),
    })
    renderApp('/projects/shop/logs')
    const header = await screen.findByRole('banner')
    const alert = await within(header).findByRole('alert')
    expect(alert.textContent).toMatch(/^Projects could not be loaded\. The LogSage API failed \(status 503\)/)
    fail = false
    await userEvent.setup().click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await within(header).findByLabelText('Project')).toBeTruthy()
  })

  it('shows the empty state and creates a project', async () => {
    let projects = [] as ReturnType<typeof project>[]
    const calls = mockApi({
      'GET /api/auth/me': me,
      'GET /api/projects': () => json(200, { projects }),
      'POST /api/projects': () => {
        projects = [project('shop-prod', 'Shop prod')]
        return json(201, { project: projects[0] })
      },
    })
    renderApp('/')
    expect(await screen.findByRole('heading', { name: 'You are not a member of any project yet' })).toBeTruthy()
    expect(within(screen.getByRole('banner')).getByText('No projects yet')).toBeTruthy()
    expect(screen.getByText(/admin add-member --project default/)).toBeTruthy()

    const user = userEvent.setup()
    await user.type(screen.getByLabelText('Name'), 'Shop prod')
    await user.type(screen.getByLabelText('Slug'), 'Shop Prod')
    await user.click(screen.getByRole('button', { name: 'Create project' }))
    expect(screen.getByText('Use 3 to 40 lowercase letters, digits or dashes.')).toBeTruthy()
    expect(calls.some((c) => c.key === 'POST /api/projects')).toBe(false)

    await user.clear(screen.getByLabelText('Slug'))
    await user.type(screen.getByLabelText('Slug'), 'shop-prod')
    await user.click(screen.getByRole('button', { name: 'Create project' }))
    await waitFor(() => expect(where()).toBe('/projects/shop-prod/logs'))
    expect(calls.find((c) => c.key === 'POST /api/projects')?.init?.body).toBe('{"name":"Shop prod","slug":"shop-prod"}')
    expect(await screen.findByLabelText('Search messages')).toBeTruthy() // the explorer
  })

  it.each([
    [409, { error: 'conflict' }, 'A project with this slug already exists. Choose another slug.'],
    [400, { error: 'invalid slug: use 3-40 lowercase letters, digits or dashes' }, 'Invalid slug: use 3-40 lowercase letters, digits or dashes.'],
    [503, { error: 'service unavailable' }, 'The LogSage API failed (status 503). Try again in a minute.'],
  ])('shows the server error %i when creating a project', async (status, body, message) => {
    mockApi({
      'GET /api/auth/me': me,
      'GET /api/projects': () => json(200, { projects: [] }),
      'POST /api/projects': () => json(status, body),
    })
    renderApp('/')
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Name'), 'Shop')
    await user.type(screen.getByLabelText('Slug'), 'shop')
    await user.click(screen.getByRole('button', { name: 'Create project' }))
    expect((await screen.findByRole('alert')).textContent).toBe(message)
    expect(where()).toBe('/')
  })

  it('says when the URL names a project the user is not in', async () => {
    mockApi({ 'GET /api/auth/me': me, 'GET /api/projects': () => json(200, { projects: [project('shop')] }) })
    renderApp(`/projects/${encodeURIComponent('<img src=x onerror=alert(1)>')}/logs`)
    expect(await screen.findByRole('heading', { name: 'Project not found' })).toBeTruthy()
    expect(document.querySelector('main img')).toBeNull()
  })
})

describe('shell', () => {
  it('has the focus order skip link, logo, project, Logs, API keys, Commands, account', async () => {
    mockApi({ 'GET /api/auth/me': me, 'GET /api/projects': () => json(200, { projects: [project('shop', 'Shop')] }) })
    renderApp('/projects/shop/logs')
    await screen.findByLabelText('Project')
    const user = userEvent.setup()
    await user.tab()
    expect(document.activeElement?.textContent).toBe('Skip to content')
    await user.tab()
    expect(document.activeElement).toBe(screen.getByRole('link', { name: 'LogSage' }))
    await user.tab()
    expect(document.activeElement).toBe(screen.getByLabelText('Project'))
    await user.tab()
    expect(document.activeElement).toBe(screen.getByRole('link', { name: 'Logs' }))
    expect(document.activeElement?.getAttribute('aria-current')).toBe('page')
    await user.tab()
    expect(document.activeElement).toBe(screen.getByRole('link', { name: 'API keys' }))
    await user.tab()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Commands' }))
    await user.tab()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Account' }))
    expect(screen.getByText('Ctrl K').tagName).toBe('KBD')
  })

  it('signs out from the account menu', async () => {
    const calls = mockApi({
      'GET /api/auth/me': me,
      'GET /api/projects': () => json(200, { projects: [project('shop')] }),
      'POST /api/auth/logout': () => json(204),
    })
    renderApp('/projects/shop/logs')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Account' }))
    expect(screen.getByText('Signed in as alice@example.com')).toBeTruthy()
    await user.keyboard('{Escape}')
    expect(screen.queryByText('Signed in as alice@example.com')).toBeNull()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Account' }))
    await user.click(screen.getByRole('button', { name: 'Account' }))
    await user.click(screen.getByRole('menuitem', { name: 'Sign out' }))
    await waitFor(() => expect(where()).toBe('/login'))
    expect(calls.some((c) => c.key === 'POST /api/auth/logout')).toBe(true)
  })
})
