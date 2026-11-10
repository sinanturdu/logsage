import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Link, Route, Routes } from 'react-router'
import { ApiError } from './api/client'
import { RequireAuth, SessionWatcher } from './auth/session'
import { RouteTitle } from './components/RouteTitle'
import { Shell } from './components/Shell'
import { HomePage } from './pages/HomePage'
import { LoginPage } from './pages/LoginPage'
import { ExplorerPage } from './explorer/ExplorerPage'
import { KeysPage } from './keys/KeysPage'
import { GroupDetailRoute } from './groups/GroupDetailPage'
import { GroupsPage } from './groups/GroupsPage'
import { AlertsPage } from './alerts/AlertsPage'
import { RuleDetailPage } from './alerts/RuleDetailPage'
import { IncidentsPage } from './incidents/IncidentsPage'
import { IncidentDetailRoute } from './incidents/IncidentDetailPage'

export function newQueryClient(opts: { retries?: number } = {}): QueryClient {
  const retries = opts.retries ?? 2
  return new QueryClient({
    defaultOptions: {
      queries: {
        // Retry only what may succeed later: network errors and 5xx.
        retry: (count, err) =>
          count < retries && err instanceof ApiError && (err.kind === 'network' || err.kind === 'server'),
        refetchOnWindowFocus: false,
      },
      // Writes are never retried automatically (a test send, a delete...): only reads are.
      mutations: { retry: false },
    },
  })
}

function NotFound() {
  return (
    <div className="panel">
      <h1>Page not found</h1>
      <p>
        This address does not exist in LogSage. <Link to="/">Go to your projects</Link>.
      </p>
    </div>
  )
}

/** The routes; the router itself is provided by main.tsx (or a test). */
export function App({ client }: { client: QueryClient }) {
  return (
    <QueryClientProvider client={client}>
      <SessionWatcher />
      <RouteTitle />
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route
          element={
            <RequireAuth>
              <Shell />
            </RequireAuth>
          }
        >
          <Route index element={<HomePage />} />
          <Route path="/projects/:slug/logs" element={<ExplorerPage />} />
          <Route path="/projects/:slug/groups" element={<GroupsPage />} />
          <Route path="/projects/:slug/groups/:fingerprint" element={<GroupDetailRoute />} />
          <Route path="/projects/:slug/alerts" element={<AlertsPage />} />
          <Route path="/projects/:slug/alerts/rules/:id" element={<RuleDetailPage />} />
          <Route path="/projects/:slug/incidents" element={<IncidentsPage />} />
          <Route path="/projects/:slug/incidents/:number" element={<IncidentDetailRoute />} />
          <Route path="/projects/:slug/keys" element={<KeysPage />} />
          <Route path="*" element={<NotFound />} />
        </Route>
      </Routes>
    </QueryClientProvider>
  )
}
