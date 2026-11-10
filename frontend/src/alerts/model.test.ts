import { describe, expect, it } from 'vitest'
import {
  RULE_TYPES,
  conditionSummary,
  durationSeconds,
  emptyRuleForm,
  fieldForServerMessage,
  formFromRule,
  humanDuration,
  parseAlertsState,
  parseGoDuration,
  previewSentences,
  ruleBody,
  serializeAlertsState,
  toDurationValue,
  typeFields,
  validateRule,
  type RuleForm,
} from './model'
import { rule } from './testing'

const form = (over: Partial<RuleForm> = {}): RuleForm => ({ ...emptyRuleForm(), name: 'errors', channels: ['c1'], ...over })

describe('durations', () => {
  it('parses Go durations and formats them in words', () => {
    expect(parseGoDuration('5m0s')).toBe(300)
    expect(parseGoDuration('1h30m0s')).toBe(5400)
    expect(parseGoDuration('30s')).toBe(30)
    expect(parseGoDuration('0s')).toBe(0)
    expect(parseGoDuration('168h0m0s')).toBe(604800)
    expect(parseGoDuration('5 minutes')).toBeNull()
    expect(parseGoDuration('')).toBeNull()
    expect(humanDuration(300)).toBe('5 minutes')
    expect(humanDuration(60)).toBe('1 minute')
    expect(humanDuration(5400)).toBe('1 hour 30 minutes')
    expect(humanDuration(172800)).toBe('2 days')
    expect(toDurationValue(7200)).toEqual({ amount: '2', unit: 'h' })
    expect(toDurationValue(90)).toEqual({ amount: '90', unit: 's' })
    expect(durationSeconds({ amount: '2', unit: 'h' })).toBe(7200)
    expect(durationSeconds({ amount: '1.5', unit: 'h' })).toBeNull()
    expect(durationSeconds({ amount: '-1', unit: 'm' })).toBeNull()
  })
})

describe('the rule form', () => {
  it('shows only the fields of each type', () => {
    expect(typeFields('error_rate')).toEqual(['threshold', 'minLogs', 'window', 'services'])
    expect(typeFields('log_volume')).toEqual(['op', 'count', 'window', 'services', 'levels'])
    expect(typeFields('new_group')).toEqual(['minEvents', 'window', 'services'])
    expect(typeFields('regression')).toEqual(['window', 'services'])
    expect(typeFields('new_groups_per_hour')).toEqual(['maxGroups'])
  })

  it('mirrors the server limits', () => {
    expect(validateRule(form())).toEqual({})
    const at = (over: Partial<RuleForm>) => validateRule(form(over))
    expect(at({ name: ' ' }).name).toBe('Enter a name.')
    expect(at({ name: 'x'.repeat(100) }).name).toBeUndefined()
    expect(at({ name: 'x'.repeat(101) }).name).toBe('Use at most 100 characters.')
    expect(at({ note: 'x'.repeat(500) }).note).toBeUndefined()
    expect(at({ note: 'x'.repeat(501) }).note).toBe('Use at most 500 characters.')
    expect(at({ interval: { amount: '29', unit: 's' } }).interval).toBe('Enter a duration between 30 seconds and 1 hour.')
    expect(at({ interval: { amount: '30', unit: 's' } }).interval).toBeUndefined()
    expect(at({ interval: { amount: '61', unit: 'm' } }).interval).toBeDefined()
    expect(at({ window: { amount: '59', unit: 's' } }).window).toBe('Enter a duration between 1 minute and 1 day.')
    expect(at({ window: { amount: '24', unit: 'h' } }).window).toBeUndefined()
    expect(at({ window: { amount: '25', unit: 'h' } }).window).toBeDefined()
    expect(at({ for: { amount: '0', unit: 'm' } }).for).toBeUndefined()
    expect(at({ for: { amount: '25', unit: 'h' } }).for).toBeDefined()
    expect(at({ clearFor: { amount: '61', unit: 'm' } }).clearFor).toBeDefined()
    expect(at({ repeatOn: true, repeat: { amount: '4', unit: 'm' } }).repeat).toBe('Enter a duration between 5 minutes and 7 days.')
    expect(at({ repeatOn: true, repeat: { amount: '7', unit: 'd' } }).repeat).toBeUndefined()
    expect(at({ repeatOn: true, repeat: { amount: '8', unit: 'd' } }).repeat).toBeDefined()
    expect(at({ repeatOn: false, repeat: { amount: '1', unit: 's' } }).repeat).toBeUndefined()
    expect(at({ channels: [] }).channels).toBe('Choose at least one channel.')
    expect(at({ channels: Array.from({ length: 11 }, (_, i) => `c${i}`) }).channels).toBe('Choose at most 10 channels.')
    expect(at({ threshold: '0.05' }).threshold).toBe('Enter a percentage between 0.1 and 100.')
    expect(at({ threshold: '100' }).threshold).toBeUndefined()
    expect(at({ threshold: 'abc' }).threshold).toBeDefined()
    expect(at({ minLogs: '9' }).minLogs).toBe('Enter a whole number between 10 and 1,000,000.')
    expect(at({ services: Array.from({ length: 21 }, (_, i) => `s${i}`) }).services).toBe('Choose at most 20 services.')
    const lv = (over: Partial<RuleForm>) => validateRule(form({ type: 'log_volume', ...over }))
    expect(lv({ op: 'below', count: '0' }).count).toContain('at least 1')
    expect(lv({ op: 'above', count: '0' }).count).toBeUndefined()
    expect(lv({ count: '1000000001' }).count).toBeDefined()
    expect(validateRule(form({ type: 'new_group', minEvents: '0' })).minEvents).toBeDefined()
    expect(validateRule(form({ type: 'new_groups_per_hour', maxGroups: '10001' })).maxGroups).toBe('Enter a whole number between 1 and 10,000.')
    // Another type's fields are not checked.
    expect(validateRule(form({ type: 'regression', threshold: 'x', minLogs: 'x' }))).toEqual({})
  })

  it('sends only the chosen type parameters, as the API expects', () => {
    expect(ruleBody(form({ threshold: '20', minLogs: '50', services: ['payments'], levels: ['info'], for: { amount: '2', unit: 'm' } }))).toEqual({
      name: 'errors',
      note: '',
      type: 'error_rate',
      params: { threshold_percent: 20, min_logs: 50, services: ['payments'] },
      interval: '60s',
      window: '300s',
      for: '120s',
      clear_for: '60s',
      repeat: '0s',
      enabled: true,
      channels: ['c1'],
      open_incident: false,
      incident_severity: 'high',
    })
    expect(ruleBody(form({ type: 'log_volume', op: 'below', count: '1', levels: ['error'] })).params).toEqual({ op: 'below', count: 1, levels: ['error'] })
    expect(ruleBody(form({ type: 'new_group', minEvents: '3' })).params).toEqual({ min_events: 3 })
    expect(ruleBody(form({ type: 'regression' })).params).toEqual({})
    const g = ruleBody(form({ type: 'new_groups_per_hour', maxGroups: '40', repeatOn: true, repeat: { amount: '2', unit: 'h' } }))
    expect(g.params).toEqual({ max_groups: 40 })
    expect(g.window).toBe('1h')
    expect(g.repeat).toBe('7200s')
  })

  it('round-trips a rule read from the API', () => {
    const f = formFromRule(rule('r1', { name: 'n', params: { threshold_percent: 12.5, min_logs: 80, services: ['a'] }, repeat: '1h0m0s' }))
    expect(f).toMatchObject({ threshold: '12.5', minLogs: '80', services: ['a'], window: { amount: '5', unit: 'm' }, for: { amount: '2', unit: 'm' }, repeatOn: true, repeat: { amount: '1', unit: 'h' } })
    expect(ruleBody(f).params).toEqual({ threshold_percent: 12.5, min_logs: 80, services: ['a'] })
    expect(formFromRule(rule('r2', { repeat: '0s' })).repeatOn).toBe(false)
    expect(ruleBody(formFromRule(rule('r3', { params: { threshold_percent: 5, min_logs: 50, hysteresis_percent: 25 } }))).params.hysteresis_percent).toBe(25)
  })

  it('maps server field messages to fields', () => {
    expect(fieldForServerMessage('window: must be between 1m and 24h, in whole seconds')).toBe('window')
    expect(fieldForServerMessage('threshold_percent: must be between 0.1 and 100')).toBe('threshold')
    expect(fieldForServerMessage('clear_for: must be between 0 and 1h')).toBe('clearFor')
    expect(fieldForServerMessage('channels: unknown channel for this project')).toBe('channels')
    expect(fieldForServerMessage('something else')).toBeNull()
  })
})

describe('sentences', () => {
  it('previews when an error rate rule fires and resolves', () => {
    const p = previewSentences(form({ threshold: '20', minLogs: '50', for: { amount: '2', unit: 'm' } }), ['Slack ops'])
    expect(p.fires).toBe(
      'Fires when error and fatal logs are at least 20% of all logs over 5 minutes, with at least 50 logs, for at least 2 minutes. Notifies: Slack ops.',
    )
    expect(p.resolves).toBe('Resolves when the share falls below 18% (10% under the threshold) or there are fewer than 50 logs, for at least 1 minute.')
  })

  it('previews every type, services, repeat and the missing channel', () => {
    const silence = previewSentences(form({ type: 'log_volume', op: 'below', count: '1', window: { amount: '10', unit: 'm' }, services: ['api'] }), ['a', 'b'])
    expect(silence.fires).toBe('Fires when there are no logs in service api over 10 minutes. Notifies: a and b.')
    expect(silence.resolves).toBe('Resolves when logs arrive again, for at least 1 minute.')
    expect(previewSentences(form({ type: 'log_volume', op: 'above', count: '1000' }), []).fires).toBe(
      'Fires when there are more than 1,000 logs over 5 minutes. Choose a channel to notify.',
    )
    expect(previewSentences(form({ type: 'log_volume', op: 'above', count: '1000' }), []).resolves).toContain('at most 900 logs')
    expect(previewSentences(form({ type: 'new_group', minEvents: '1', services: ['a', 'b', 'c'] }), ['x']).fires).toBe(
      'Fires once for each error group seen for the first time in services a, b and c over 5 minutes, with at least 1 event. Notifies: x.',
    )
    expect(previewSentences(form({ type: 'regression' }), ['x']).fires).toBe('Fires once each time a resolved error group is seen again over 5 minutes. Notifies: x.')
    const g = previewSentences(form({ type: 'new_groups_per_hour', maxGroups: '50', repeatOn: true, repeat: { amount: '1', unit: 'h' } }), ['x'])
    expect(g.fires).toBe('Fires when more than 50 new error groups appear in one hour. Notifies: x. While firing, notifies again every 1 hour.')
    expect(g.resolves).toContain('at most 45 new groups')
    // clear_for 0 uses the interval, like the server.
    expect(previewSentences(form({ clearFor: { amount: '0', unit: 'm' }, interval: { amount: '2', unit: 'm' } }), ['x']).resolves).toContain('for at least 2 minutes')
  })

  it('summarizes conditions for the table', () => {
    expect(conditionSummary('error_rate', { threshold_percent: 20, min_logs: 50 }, 300)).toBe('Errors ≥ 20% of logs over 5 minutes, at least 50 logs')
    expect(conditionSummary('log_volume', { op: 'below', count: 1 }, 600)).toBe('No logs over 10 minutes')
    expect(conditionSummary('log_volume', { op: 'above', count: 5000, services: ['api'] }, 300)).toBe('Logs > 5,000 over 5 minutes in service api')
    expect(conditionSummary('new_group', { min_events: 1 }, 900)).toBe('New error group with ≥ 1 event over 15 minutes')
    expect(conditionSummary('regression', {}, 3600)).toBe('Resolved group seen again over 1 hour')
    expect(conditionSummary('new_groups_per_hour', { max_groups: 50 }, 3600)).toBe('More than 50 new groups in an hour')
    for (const t of RULE_TYPES) expect(conditionSummary(t, {}, 60)).not.toBe('')
  })
})

describe('URL state', () => {
  it('parses and serializes the tab, the activity filter and create', () => {
    expect(parseAlertsState(new URLSearchParams(''))).toEqual({ tab: 'rules', rule: '', create: '' })
    expect(parseAlertsState(new URLSearchParams('tab=activity&rule=3f2a-11'))).toEqual({ tab: 'activity', rule: '3f2a-11', create: '' })
    expect(parseAlertsState(new URLSearchParams('tab=nope&rule=<x>&create=evil'))).toEqual({ tab: 'rules', rule: '', create: '' })
    expect(serializeAlertsState({ tab: 'rules', rule: 'x', create: '' }).toString()).toBe('')
    expect(serializeAlertsState({ tab: 'activity', rule: 'ab', create: '' }).toString()).toBe('tab=activity&rule=ab')
    expect(serializeAlertsState({ tab: 'channels', rule: '', create: 'channel' }).toString()).toBe('tab=channels&create=channel')
  })
})
