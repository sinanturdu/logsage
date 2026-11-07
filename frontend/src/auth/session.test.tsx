import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { projectsKey } from '../api/projects'
import { alice, json, mockApi, project, renderApp } from '../test/util'
import { safeNext } from './session'

async function signIn() {
  const user = userEvent.setup()
  await user.type(await screen.findByLabelText('Email'), 'alice@example.com')
  await user.type(screen.getByLabelText('Password'), 'a long passphrase')
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
}

const where = () => screen.getByTestId('location').textContent

describe('session', () => {
  it('sends a visitor without a session to sign-in and back to the same URL', async () => {
    const routes = {
      'GET /api/auth/me': () => json(401, { error: 'unauthorized' }),
      'POST /api/auth/login': () => json(200, alice),
      'GET /api/projects': () => json(200, { projects: [project('shop', 'Shop')] }),
    }
    mockApi(routes)
    renderApp('/projects/shop/logs?level=error')
    await waitFor(() => expect(where()).toBe('/login?next=%2Fprojects%2Fshop%2Flogs%3Flevel%3Derror'))
    expect(screen.queryByText('Your session ended. Sign in again to continue.')).toBeNull() // never signed in
    await signIn()
    await waitFor(() => expect(where()).toBe('/projects/shop/logs?level=error'))
    expect(await screen.findByRole('heading', { name: 'Logs for Shop' })).toBeTruthy()
  })

  it('on a 401 after sign-in says the session ended and returns afterwards', async () => {
    const routes: Parameters<typeof mockApi>[0] = {
      'GET /api/auth/me': () => json(200, alice),
      'GET /api/projects': () => json(200, { projects: [project('shop', 'Shop')] }),
      'POST /api/auth/login': () => json(200, alice),
    }
    mockApi(routes)
    const { client } = renderApp('/projects/shop/logs?q=timeout')
    expect(await screen.findByRole('heading', { name: 'Logs for Shop' })).toBeTruthy()

    routes['GET /api/projects'] = () => json(401, { error: 'unauthorized' }) // revoked elsewhere
    await client.invalidateQueries({ queryKey: projectsKey })
    expect(await screen.findByText('Your session ended. Sign in again to continue.')).toBeTruthy()
    expect(where()).toBe('/login?next=%2Fprojects%2Fshop%2Flogs%3Fq%3Dtimeout&expired=1')

    routes['GET /api/projects'] = () => json(200, { projects: [project('shop', 'Shop')] })
    await signIn()
    await waitFor(() => expect(where()).toBe('/projects/shop/logs?q=timeout'))
  })

  it('accepts only same-app return paths', () => {
    expect(safeNext('/projects/a/logs?x=1')).toBe('/projects/a/logs?x=1')
    for (const bad of [null, '', 'https://evil.example', '//evil.example', '/\\evil.example', 'javascript:alert(1)', '/login?next=/x']) {
      expect(safeNext(bad)).toBe('/')
    }
  })
})
