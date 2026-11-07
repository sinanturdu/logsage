import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Link, Route, Routes } from 'react-router'
import { ApiError } from './api/client'
import { RequireAuth, SessionWatcher } from './auth/session'
import { Shell } from './components/Shell'
import { HomePage } from './pages/HomePage'
import { LoginPage } from './pages/LoginPage'
import { LogsPage } from './pages/LogsPage'

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
          <Route path="/projects/:slug/logs" element={<LogsPage />} />
          <Route path="*" element={<NotFound />} />
        </Route>
      </Routes>
    </QueryClientProvider>
  )
}
