import { useId } from 'react'
import { useNavigate, useParams } from 'react-router'
import { errorMessage } from '../api/client'
import { useProjects } from '../api/projects'

// The project switcher in the shell header. Its states: loading, error
// (with retry), no projects (the page shows the create form) and the list.
export function ProjectSwitcher() {
  const projects = useProjects()
  const { slug } = useParams()
  const navigate = useNavigate()
  const id = useId()

  if (projects.isPending) {
    return (
      <span className="project-switcher" role="status">
        Loading projects…
      </span>
    )
  }
  if (projects.isError) {
    return (
      <span className="project-switcher" role="alert">
        Projects could not be loaded. {errorMessage(projects.error)}
        <button type="button" className="button button-quiet" onClick={() => void projects.refetch()}>
          Retry
        </button>
      </span>
    )
  }
  if (projects.data.length === 0) {
    return <span className="project-switcher">No projects yet</span>
  }
  const current = projects.data.some((p) => p.slug === slug) ? (slug ?? '') : ''
  return (
    <span className="project-switcher">
      <label htmlFor={id} className="visually-hidden">
        Project
      </label>
      <select
        id={id}
        value={current}
        onChange={(e) => navigate(`/projects/${encodeURIComponent(e.target.value)}/logs`)}
      >
        {current === '' && <option value="">Choose a project</option>}
        {projects.data.map((p) => (
          <option key={p.id} value={p.slug}>
            {p.name} ({p.slug})
          </option>
        ))}
      </select>
    </span>
  )
}
