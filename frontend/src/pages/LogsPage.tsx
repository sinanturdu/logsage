import { useParams } from 'react-router'
import { useProjects } from '../api/projects'
import { LogText } from '../components/LogText'

// Development placeholder: the B2 log explorer arrives in package I2b.
export function LogsPage() {
  const { slug = '' } = useParams()
  const projects = useProjects()
  const project = projects.data?.find((p) => p.slug === slug)

  if (projects.isSuccess && !project) {
    return (
      <div className="panel" role="alert">
        <h1>Project not found</h1>
        <p>
          There is no project <LogText text={slug} /> that you are a member of. Choose a project in the header.
        </p>
      </div>
    )
  }
  return (
    <section className="placeholder" aria-labelledby="logs-title">
      <span className="tag">Development placeholder</span>
      <h1 id="logs-title" className="panel">
        Logs for <LogText text={project?.name ?? slug} mono={false} />
      </h1>
      <p>The log explorer (timeline, filters, rows, live tail) arrives in package I2b. The API endpoints it uses are ready:</p>
      <p>
        <code>/api/projects/{slug}/logs</code>, <code>/histogram</code>, <code>/facets</code> and <code>/tail</code>.
      </p>
    </section>
  )
}
