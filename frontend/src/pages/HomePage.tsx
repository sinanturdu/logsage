import { Navigate } from 'react-router'
import { errorMessage } from '../api/client'
import { useProjects } from '../api/projects'
import { CreateProjectForm } from '../components/CreateProjectForm'

// "/" opens the first project's log explorer, or the empty state.
export function HomePage() {
  const projects = useProjects()
  if (projects.isPending) return <p role="status">Loading projects…</p>
  if (projects.isError) {
    return (
      <div className="panel" role="alert">
        <h1>Projects could not be loaded</h1>
        <p>{errorMessage(projects.error)}</p>
        <button type="button" className="button" onClick={() => void projects.refetch()}>
          Try again
        </button>
      </div>
    )
  }
  const first = projects.data[0]
  if (first) return <Navigate to={`/projects/${encodeURIComponent(first.slug)}/logs`} replace />
  return (
    <section className="panel" aria-labelledby="no-projects">
      <h1 id="no-projects">You are not a member of any project yet</h1>
      <p>Create a project to get API keys and start sending logs, or ask a project owner to add you.</p>
      <p className="field-hint">
        The built-in <code>default</code> project has no members until an operator runs{' '}
        <code>admin add-member --project default --email you@example.com --role owner</code>.
      </p>
      <h2>Create project</h2>
      <CreateProjectForm />
    </section>
  )
}
