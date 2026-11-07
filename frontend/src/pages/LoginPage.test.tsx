import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { alice, json, mockApi, project, renderApp } from '../test/util'

const unauth = () => json(401, { error: 'unauthorized' })

describe('login form', () => {
  it('validates before calling the API', async () => {
    const calls = mockApi({ 'GET /api/auth/me': unauth })
    renderApp('/login')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(screen.getByText('Enter your email address.')).toBeTruthy()
    expect(screen.getByText('Enter your password.')).toBeTruthy()
    expect(screen.getByLabelText('Email').getAttribute('aria-invalid')).toBe('true')
    await user.type(screen.getByLabelText('Email'), 'not-an-email')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(screen.getByText('Enter an email address such as you@example.com.')).toBeTruthy()
    expect(calls.filter((c) => c.key === 'POST /api/auth/login')).toHaveLength(0)
  })

  it('says only that email or password is wrong, and clears the password', async () => {
    const calls = mockApi({ 'POST /api/auth/login': unauth })
    renderApp('/login')
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('Email'), 'alice@example.com')
    await user.type(screen.getByLabelText('Password'), 'not the password')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect((await screen.findByRole('alert')).textContent).toBe('Email or password is wrong. Check both and try again.')
    expect((screen.getByLabelText('Password') as HTMLInputElement).value).toBe('')
    expect(calls[0]?.init?.body).toBe('{"email":"alice@example.com","password":"not the password"}')
  })

  it('shows the wait time when sign-in is throttled', async () => {
    mockApi({ 'POST /api/auth/login': () => json(429, { error: 'too many requests' }, { 'Retry-After': '12' }) })
    renderApp('/login')
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('Email'), 'alice@example.com')
    await user.type(screen.getByLabelText('Password'), 'x')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect((await screen.findByRole('alert')).textContent).toBe('Too many sign-in attempts. Try again in 12 seconds.')
  })

  it('explains an unreachable API', async () => {
    mockApi({ 'POST /api/auth/login': () => Promise.reject(new TypeError('Failed to fetch')) })
    renderApp('/login')
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('Email'), 'alice@example.com')
    await user.type(screen.getByLabelText('Password'), 'x')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect((await screen.findByRole('alert')).textContent).toMatch(/^Cannot reach the LogSage API/)
  })

  it('signs in without storing anything in the browser', async () => {
    mockApi({
      'POST /api/auth/login': () => json(200, alice),
      'GET /api/projects': () => json(200, { projects: [project('shop', 'Shop')] }),
    })
    renderApp('/login')
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('Email'), 'alice@example.com')
    await user.type(screen.getByLabelText('Password'), 'a long passphrase')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/projects/shop/logs'))
    expect(window.localStorage.length).toBe(0)
    expect(window.sessionStorage.length).toBe(0)
    expect(document.cookie).toBe('')
  })

  it('has the focus order email, password, sign in', async () => {
    mockApi({})
    renderApp('/login')
    const user = userEvent.setup()
    expect(document.activeElement).toBe(screen.getByLabelText('Email'))
    await user.tab()
    expect(document.activeElement).toBe(screen.getByLabelText('Password'))
    await user.tab()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Sign in' }))
  })
})
