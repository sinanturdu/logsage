import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { json, renderApp } from '../test/util'
import { mockAlerts } from '../alerts/testing'
import { KINDS as CHANNEL_KINDS } from '../alerts/ChannelDialogs'
import { RULE_TYPES, TYPE_HINT, TYPE_LABEL } from '../alerts/model'
import { KINDS as KEY_KINDS } from '../keys/KeysPage'

afterEach(() => vi.unstubAllGlobals())

/** The name is the radio's name, the description its description, each on its own line. */
function expectChoice(dialog: HTMLElement, label: string, description: string) {
  const radio = within(dialog).getByRole('radio', { name: label, description })
  const text = radio.closest('.choice')?.querySelector('.choice-text')
  expect(Array.from(text?.children ?? []).map((c) => [c.className, c.textContent])).toEqual([
    ['choice-name', label],
    ['choice-desc', description],
  ])
}

describe('radio options', () => {
  it('rule types and channel types: the name, then the muted description', async () => {
    mockAlerts({})
    renderApp('/projects/shop/alerts')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Create rule' }))
    const rules = await screen.findByRole('dialog', { name: 'Create an alert rule' })
    for (const t of RULE_TYPES) expectChoice(rules, TYPE_LABEL[t], TYPE_HINT[t])
    await user.keyboard('{Escape}')
    await user.click(screen.getByRole('tab', { name: 'Channels' }))
    await user.click(await screen.findByRole('button', { name: 'Create channel' }))
    const channels = await screen.findByRole('dialog', { name: 'Create a channel' })
    for (const k of CHANNEL_KINDS) expectChoice(channels, k.label, k.description)
  })

  it('API key kinds use the same pattern', async () => {
    mockAlerts({ 'GET /api/projects/shop/keys': () => json(200, { keys: [] }) })
    renderApp('/projects/shop/keys')
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Create key' }))
    const dialog = await screen.findByRole('dialog', { name: 'Create an API key' })
    for (const k of KEY_KINDS) expectChoice(dialog, k.label, k.description)
  })
})
