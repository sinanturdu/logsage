import { useInfiniteQuery, useQueries, useQuery } from '@tanstack/react-query'
import { ApiError, errorMessage, request } from '../api/client'
import type { RuleBody, RuleParams, RuleType } from './model'

// The alert endpoints of cmd/api (ADR 0024, contract in docs/architecture.md).
// A webhook signing secret comes back only from createChannel and
// rotateSecret: callers keep it in component state and never pass it to
// React Query, the URL, storage or the console (ADR 0025).

export type RuleState = 'ok' | 'pending' | 'firing' | 'resolved'
export type Rule = {
  id: string
  name: string
  note: string
  type: RuleType
  params: RuleParams
  interval: string
  window: string
  for: string
  clear_for: string
  repeat: string
  enabled: boolean
  channels: string[]
  version: number
  next_eval_at: string
  state: RuleState
  firing: number
  pending: number
  last_change_at: string | null
}
export type ChannelKind = 'webhook' | 'slack'
export type Channel = {
  id: string
  name: string
  kind: ChannelKind
  /** Scheme and host only; never the path or a secret. */
  url_host: string
  include_sample: boolean
  created_at: string
  updated_at: string
}
export type StateRecord = {
  rule_id: string
  rule_name: string
  dedup_key: string
  state: 'pending' | 'firing'
  since: string
  value: number
  fired_at: string | null
  fingerprint: string
  template: string
}
export type AlertEvent = {
  id: number
  rule_id: string | null
  rule_name: string
  rule_type: string
  dedup_key: string
  from: string
  to: string
  value: number
  threshold: number
  window_start: string
  window_end: string
  fingerprint: string
  template: string
  reason: string
  at: string
}
export type DeliveryStatus = 'pending' | 'sending' | 'sent' | 'failed' | 'cancelled'
export type Delivery = {
  id: number
  channel_id: string | null
  channel_name: string
  rule_id: string
  rule_name: string
  event_id: number | null
  event: string
  status: DeliveryStatus
  attempts: number
  response_status: number | null
  /** A closed category, never text from the receiver (ADR 0024 addendum). */
  error_category: ErrorCategory | ''
  test: boolean
  idempotency_key: string
  created_at: string
  next_attempt_at: string
  sent_at: string | null
}

export const ERROR_CATEGORIES = ['http_4xx', 'http_5xx', 'timeout', 'connect_error', 'tls_error', 'blocked_address', 'rate_limited', 'canceled', 'unknown'] as const
export type ErrorCategory = (typeof ERROR_CATEGORIES)[number]

/**
 * A delivery failure in one plain sentence, from the status code and the
 * category only: LogSage never stores or shows what a receiver answered.
 */
export function failureSentence(d: Pick<Delivery, 'response_status' | 'error_category'>): string {
  const code = d.response_status
  switch (d.error_category) {
    case '':
      return ''
    case 'timeout':
      return code ? `The receiver answered with HTTP ${code} (request timeout).` : 'The receiver did not answer in time.'
    case 'connect_error':
      return 'LogSage could not connect to the receiver.'
    case 'tls_error':
      return "The receiver's TLS certificate could not be verified."
    case 'blocked_address':
      return 'This address is not allowed any more.'
    case 'rate_limited':
      return code ? `The receiver answered with HTTP ${code} (too many requests).` : "Waiting: the channel's limit of sends per minute was reached."
    case 'canceled':
      return 'Cancelled: the rule or the channel changed.'
    default:
      return code ? `The receiver answered with HTTP ${code}.` : 'The delivery failed.'
  }
}

export const HISTORY_PAGE = 50
export const NAV_POLL_MS = 30_000

const base = (slug: string) => `/api/projects/${encodeURIComponent(slug)}/alerts`
export const alertsKey = (slug: string) => ['alerts', slug] as const
export const rulesKey = (slug: string) => ['alerts', slug, 'rules'] as const
export const channelsKey = (slug: string) => ['alerts', slug, 'channels'] as const
export const statesKey = (slug: string) => ['alerts', slug, 'states'] as const

/** 503 because alerts are turned off or the secret key is missing on the server. */
export function isAlertsOff(err: unknown): boolean {
  return err instanceof ApiError && err.status === 503 && /^alerts are /.test(err.serverMessage ?? '')
}

/** Retry network errors and 5xx twice, but never the "turned off" answer. */
const retry = (count: number, err: unknown) =>
  count < 2 && err instanceof ApiError && (err.kind === 'network' || err.kind === 'server') && !isAlertsOff(err)

export function useRules(slug: string) {
  return useQuery({
    queryKey: rulesKey(slug),
    queryFn: ({ signal }) => request<{ rules: Rule[] }>('GET', `${base(slug)}/rules`, { signal }),
    select: (d) => d.rules,
    retry,
  })
}

export function useRule(slug: string, id: string) {
  return useQuery({
    queryKey: ['alerts', slug, 'rule', id],
    queryFn: ({ signal }) => request<{ rule: Rule }>('GET', `${base(slug)}/rules/${encodeURIComponent(id)}`, { signal }),
    select: (d) => d.rule,
    retry,
  })
}

export function useChannels(slug: string) {
  return useQuery({
    queryKey: channelsKey(slug),
    queryFn: ({ signal }) => request<{ channels: Channel[] }>('GET', `${base(slug)}/channels`, { signal }),
    select: (d) => d.channels,
    retry,
  })
}

export function useStates(slug: string) {
  return useQuery({
    queryKey: statesKey(slug),
    queryFn: ({ signal }) => request<{ states: StateRecord[] }>('GET', `${base(slug)}/states`, { signal }),
    select: (d) => d.states,
    retry,
  })
}

/** The number of rules with at least one firing state. */
export const firingRules = (states: readonly StateRecord[]) => new Set(states.filter((s) => s.state === 'firing').map((s) => s.rule_id)).size

/**
 * The firing count for the nav, polled every 30 s. React Query does not
 * poll while the tab is hidden (refetchIntervalInBackground is off) and
 * refetches when it is shown again; a 429 waits for its Retry-After.
 */
export function useFiringCount(slug: string, enabled: boolean) {
  return useQuery({
    queryKey: statesKey(slug),
    enabled,
    queryFn: ({ signal }) => request<{ states: StateRecord[] }>('GET', `${base(slug)}/states`, { signal }),
    select: (d) => firingRules(d.states),
    retry: false,
    refetchOnWindowFocus: true,
    refetchIntervalInBackground: false,
    refetchInterval: (q) => {
      const err = q.state.error
      if (isAlertsOff(err)) return false
      if (err instanceof ApiError && err.kind === 'rate_limited') return Math.max(NAV_POLL_MS, (err.retryAfter ?? 1) * 1000)
      return NAV_POLL_MS
    },
  })
}

type Page<K extends string, T> = { [k in K]: T[] } & { next_before: number | null }

export function useEvents(slug: string, rule: string) {
  return useInfiniteQuery({
    queryKey: ['alerts', slug, 'events', rule],
    initialPageParam: 0,
    queryFn: ({ signal, pageParam }) => {
      const p = new URLSearchParams({ limit: String(HISTORY_PAGE) })
      if (rule) p.set('rule', rule)
      if (pageParam) p.set('before', String(pageParam))
      return request<Page<'events', AlertEvent>>('GET', `${base(slug)}/events?${p.toString()}`, { signal })
    },
    getNextPageParam: (last) => last.next_before ?? undefined,
    retry,
  })
}

export function useDeliveries(slug: string, filter: { rule?: string; status?: DeliveryStatus }) {
  return useInfiniteQuery({
    queryKey: ['alerts', slug, 'notifications', filter.rule ?? '', filter.status ?? ''],
    initialPageParam: 0,
    queryFn: ({ signal, pageParam }) => {
      const p = new URLSearchParams({ limit: String(HISTORY_PAGE) })
      if (filter.rule) p.set('rule', filter.rule)
      if (filter.status) p.set('status', filter.status)
      if (pageParam) p.set('before', String(pageParam))
      return request<Page<'notifications', Delivery>>('GET', `${base(slug)}/notifications?${p.toString()}`, { signal })
    },
    getNextPageParam: (last) => last.next_before ?? undefined,
    retry,
  })
}

/** Each channel's latest delivery (one small request per channel, at most 10). */
export function useLastDeliveries(slug: string, channels: readonly Channel[]) {
  return useQueries({
    queries: channels.map((c) => ({
      queryKey: ['alerts', slug, 'last-delivery', c.id],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        request<Page<'notifications', Delivery>>('GET', `${base(slug)}/notifications?channel=${encodeURIComponent(c.id)}&limit=1`, { signal }),
      select: (d: Page<'notifications', Delivery>) => d.notifications[0] ?? null,
      retry,
    })),
  })
}

export const createRule = (slug: string, body: RuleBody) => request<{ rule: Rule }>('POST', `${base(slug)}/rules`, { body })
export const updateRule = (slug: string, id: string, body: RuleBody) =>
  request<{ rule: Rule }>('PUT', `${base(slug)}/rules/${encodeURIComponent(id)}`, { body })
export const setRuleEnabled = (slug: string, id: string, enabled: boolean) =>
  request<{ rule: Rule }>('POST', `${base(slug)}/rules/${encodeURIComponent(id)}/${enabled ? 'enable' : 'disable'}`, { body: {} })
export const deleteRule = (slug: string, id: string) => request<undefined>('DELETE', `${base(slug)}/rules/${encodeURIComponent(id)}`)

export type ChannelInput = { name: string; kind: ChannelKind; url?: string; include_sample: boolean }
/** Creates a channel; a webhook's response holds the signing secret, shown once. */
export const createChannel = (slug: string, body: ChannelInput) =>
  request<{ channel: Channel; secret?: string }>('POST', `${base(slug)}/channels`, { body })
export const updateChannel = (slug: string, id: string, body: Partial<ChannelInput>) =>
  request<{ channel: Channel }>('PUT', `${base(slug)}/channels/${encodeURIComponent(id)}`, { body })
export const rotateSecret = (slug: string, id: string) =>
  request<{ channel: Channel; secret: string }>('POST', `${base(slug)}/channels/${encodeURIComponent(id)}/rotate-secret`, { body: {} })
export const deleteChannel = (slug: string, id: string) => request<undefined>('DELETE', `${base(slug)}/channels/${encodeURIComponent(id)}`)
export const testChannel = (slug: string, id: string) =>
  request<{ notification_id: number; idempotency_key: string }>('POST', `${base(slug)}/channels/${encodeURIComponent(id)}/test`, { body: {} })

/** Pages in order without duplicate ids. */
export function mergePages<T extends { id: number }, K extends string>(pages: readonly Page<K, T>[] | undefined, key: K): T[] {
  const seen = new Set<number>()
  const out: T[] = []
  for (const page of pages ?? []) {
    for (const item of page[key]) {
      if (seen.has(item.id)) continue
      seen.add(item.id)
      out.push(item)
    }
  }
  return out
}

export const NOT_ALLOWED_ADDRESS = 'This address is not allowed. LogSage only sends to public https addresses.'
export const URL_CREDENTIALS = 'Remove the user name and password from the URL. Put credentials in the path or a query parameter the receiver checks, or rely on the signature.'
export const TEST_LIMIT = (s: number) =>
  `Test sends are limited to 5 per channel every 10 minutes. Try again in ${s < 60 ? `${s} seconds` : `${Math.ceil(s / 60)} minutes`}.`

export type Problem = { text: string; retry: boolean; off?: boolean; notFound?: boolean }

const sentence = (s: string, keepField = false) => {
  const t = keepField ? s : s.replace(/^[a-z_]+:\s*/, '')
  return t.charAt(0).toUpperCase() + t.slice(1) + (/[.!?]$/.test(t) ? '' : '.')
}

/** Copy for a failed alerts request: off state, retryable failures, the rest. */
export function alertsProblem(err: unknown, what: 'list' | 'rule' = 'list'): Problem {
  if (isAlertsOff(err)) return { text: 'Alerts are turned off on this server.', retry: false, off: true }
  if (err instanceof ApiError) {
    switch (err.kind) {
      case 'network':
        return { text: errorMessage(err), retry: true }
      case 'server':
        return { text: `The LogSage API failed (status ${err.status}). Try again in a minute.`, retry: true }
      case 'rate_limited':
        return { text: `Too many requests. Try again in ${err.retryAfter ?? 1} seconds.`, retry: true }
      case 'not_found':
        return what === 'rule'
          ? { text: 'This alert rule does not exist in this project. It may have been deleted.', retry: false, notFound: true }
          : { text: 'This project was not found, or you are not a member of it. Choose a project in the header.', retry: false }
      default:
        return { text: errorMessage(err), retry: false }
    }
  }
  return { text: errorMessage(err), retry: true }
}

/** The message for a failed write: server field messages become sentences. */
export function writeError(err: unknown): string {
  if (isAlertsOff(err)) return 'Alerts are turned off on this server.'
  if (err instanceof ApiError) {
    if (err.kind === 'invalid' && err.serverMessage) return sentence(err.serverMessage)
    if (err.status === 409 && err.serverMessage) return sentence(err.serverMessage, true)
    if (err.status === 409) return 'Someone else changed this at the same time. Reload and try again.'
  }
  return errorMessage(err)
}

/**
 * Plain sentences for a refused channel URL. The SSRF refusals get one
 * sentence; a more specific server message (port, fragment, resolve) is
 * kept after it.
 */
export function channelUrlError(err: unknown): string | null {
  if (!(err instanceof ApiError) || err.kind !== 'invalid') return null
  const m = err.serverMessage ?? ''
  if (!m.startsWith('url:')) return null
  if (m.includes('user name or password')) return URL_CREDENTIALS
  if (m.includes('private or reserved') || m.includes('host is not allowed') || m.includes('must use https')) return NOT_ALLOWED_ADDRESS
  if (m.includes('required') || m.includes('not a valid URL')) return 'Enter a valid https URL, for example https://hooks.example.com/logsage.'
  return `${NOT_ALLOWED_ADDRESS} ${sentence(m)}`
}
