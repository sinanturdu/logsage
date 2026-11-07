import { useId, useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router'
import { ApiError, SLUG_PATTERN, errorMessage } from '../api/client'
import { useCreateProject } from '../api/projects'

function createError(err: unknown): string {
  if (err instanceof ApiError && err.kind === 'conflict') {
    return 'A project with this slug already exists. Choose another slug.'
  }
  return errorMessage(err)
}

type Errors = { name?: string; slug?: string }

export function CreateProjectForm() {
  const id = useId()
  const navigate = useNavigate()
  const create = useCreateProject()
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [errors, setErrors] = useState<Errors>({})

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    const next: Errors = {}
    if (!name.trim()) next.name = 'Enter a project name.'
    else if (name.trim().length > 200) next.name = 'Use at most 200 characters.'
    if (!SLUG_PATTERN.test(slug)) next.slug = 'Use 3 to 40 lowercase letters, digits or dashes.'
    setErrors(next)
    if (next.name || next.slug) return
    create.mutate(
      { name: name.trim(), slug },
      { onSuccess: ({ project }) => navigate(`/projects/${encodeURIComponent(project.slug)}/logs`) },
    )
  }

  return (
    <form onSubmit={onSubmit} noValidate aria-label="Create project">
      {create.isError && (
        <p className="form-error" role="alert">
          {createError(create.error)}
        </p>
      )}
      <div className="field">
        <label htmlFor={`${id}-name`}>Name</label>
        <input
          id={`${id}-name`}
          value={name}
          onChange={(e) => setName(e.target.value)}
          aria-invalid={errors.name ? true : undefined}
          aria-describedby={errors.name ? `${id}-name-error` : undefined}
        />
        {errors.name && (
          <p className="field-error" id={`${id}-name-error`}>
            {errors.name}
          </p>
        )}
      </div>
      <div className="field">
        <label htmlFor={`${id}-slug`}>Slug</label>
        <input
          id={`${id}-slug`}
          className="mono"
          value={slug}
          onChange={(e) => setSlug(e.target.value)}
          aria-invalid={errors.slug ? true : undefined}
          aria-describedby={`${id}-slug-hint${errors.slug ? ` ${id}-slug-error` : ''}`}
          autoCapitalize="none"
          spellCheck={false}
        />
        <p className="field-hint" id={`${id}-slug-hint`}>
          Used in URLs and API keys: 3 to 40 lowercase letters, digits or dashes.
        </p>
        {errors.slug && (
          <p className="field-error" id={`${id}-slug-error`}>
            {errors.slug}
          </p>
        )}
      </div>
      <button type="submit" className="button button-primary" disabled={create.isPending}>
        {create.isPending ? 'Creating…' : 'Create project'}
      </button>
    </form>
  )
}
