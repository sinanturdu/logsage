import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { json, renderApp } from '../test/util'
import { EMPTY_RULES, FIRST_CHANNEL } from './AlertsPage'
import { channel, mockAlerts } from './testing'

afterEach(() => vi.unstubAllGlobals())

const B = '/api/projects/shop/alerts'

/** No channels until one is created (a Slack channel: no secret dialog). */
function noChannels(role: 'owner' | 'viewer' = 'owner') {
  let channels: ReturnType<typeof channel>[] = []
  const m = mockAlerts(
    {
      [`GET ${B}/channels`]: () => json(200, { channels }),
      [`POST ${B}/channels`]: () => {
        channels = [channel('c1', { name: 'ops', kind: 'slack' })]
        return json(201, { channel: channels[0] })
      },
    },
    role,
  )
  return m
}

async function fillSlackChannel(user: ReturnType<typeof userEvent.setup>) {
  const dialog = await screen.findByRole('dialog', { name: 'Create a channel' })
  await user.click(within(dialog).getByRole('radio', { name: 'Slack' }))
  await user.type(within(dialog).getByLabelText('Name'), 'ops')
  await user.type(within(dialog).getByLabelText('URL'), 'https://hooks.slack.com/services/x')
  await user.click(within(dialog).getByRole('button', { name: 'Create channel' }))
}

describe('no channels yet', () => {
  it('the rule dialog sends the user to the channel dialog, then offers to create the rule', async () => {
    noChannels()
    renderApp('/projects/shop/alerts')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Create rule' }))
    const rules = await screen.findByRole('dialog', { name: 'Create an alert rule' })
    expect(within(rules).getByRole('button', { name: 'Create rule' }).hasAttribute('disabled')).toBe(true)
    await user.click(within(rules).getByRole('button', { name: 'Create a channel' }))

    // The rule dialog closes, the Channels tab opens with its dialog, and create=channel leaves the URL.
    expect(await screen.findByRole('dialog', { name: 'Create a channel' })).toBeTruthy()
    expect(screen.queryByRole('dialog', { name: 'Create an alert rule' })).toBeNull()
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/projects/shop/alerts?tab=channels'))
    expect(screen.getByRole('tab', { name: 'Channels' }).getAttribute('aria-selected')).toBe('true')

    await fillSlackChannel(user)
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    const status = screen.getByText(FIRST_CHANNEL).closest('[role="status"]') as HTMLElement
    expect(status).toBeTruthy()
    await user.click(within(status).getByRole('button', { name: 'Create rule' }))
    const again = await screen.findByRole('dialog', { name: 'Create an alert rule' })
    expect(within(again).getByRole('checkbox', { name: /ops/ })).toBeTruthy()
    expect(screen.queryByText(FIRST_CHANNEL)).toBeNull()
  })

  it('the empty Rules tab has the same button', async () => {
    noChannels()
    renderApp('/projects/shop/alerts')
    const panel = (await screen.findByText(EMPTY_RULES)).closest('.state-panel') as HTMLElement
    await userEvent.setup().click(within(panel).getByRole('button', { name: 'Create a channel' }))
    expect(await screen.findByRole('dialog', { name: 'Create a channel' })).toBeTruthy()
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/projects/shop/alerts?tab=channels'))
  })

  it('viewers get no button', async () => {
    noChannels('viewer')
    renderApp('/projects/shop/alerts')
    const panel = (await screen.findByText(EMPTY_RULES)).closest('.state-panel') as HTMLElement
    expect(within(panel).queryByRole('button')).toBeNull()
  })

  it('says nothing after creating a channel when one already existed', async () => {
    mockAlerts({ [`POST ${B}/channels`]: () => json(201, { channel: channel('c2', { kind: 'slack' }) }) })
    renderApp('/projects/shop/alerts?tab=channels')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Create channel' }))
    await fillSlackChannel(user)
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(screen.queryByText(FIRST_CHANNEL)).toBeNull()
  })
})
