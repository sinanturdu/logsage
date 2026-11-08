import { Link, useParams } from 'react-router'
import { parseFingerprint } from './model'

/** A page for an address that cannot be a group, or a group that does not exist. */
export function GroupNotFound({ slug, invalid }: { slug: string; invalid: boolean }) {
  return (
    <div className="panel" role="alert">
      <h1>Group not found</h1>
      <p>
        {invalid
          ? 'This address is not an error group: a group is named by 16 hexadecimal characters.'
          : 'This group does not exist in this project.'}{' '}
        <Link to={`/projects/${encodeURIComponent(slug)}/groups`}>Go to Error groups</Link>.
      </p>
    </div>
  )
}

/** /projects/:slug/groups/:fingerprint; anything but 16 hex is a 404 page. */
export function GroupDetailRoute() {
  const { slug = '', fingerprint } = useParams()
  const fp = parseFingerprint(fingerprint)
  if (!fp) return <GroupNotFound slug={slug} invalid />
  return <h1>Error group</h1>
}
