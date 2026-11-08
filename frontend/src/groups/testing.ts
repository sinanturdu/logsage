import type { Group } from './api'

// Shared fixtures for the groups tests.

export const group = (fp: string, extra: Partial<Group> = {}): Group => ({
  fingerprint: fp,
  title: 'payment gateway timeout after <n> ms',
  sample: 'payment gateway timeout after 5000 ms',
  error_type: '',
  levels: ['error'],
  services: ['payments'],
  count: 10,
  first_seen: '2026-11-07T20:00:00.000Z',
  last_seen: '2026-11-07T20:10:00.000Z',
  sparkline: [1, 2, 3],
  is_new: false,
  status: 'open',
  regressed: false,
  resolved_at: null,
  ...extra,
})

