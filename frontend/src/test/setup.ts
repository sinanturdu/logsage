import { afterEach } from 'vitest'
import { cleanup, configure } from '@testing-library/react'
import { takeCsrfViolations } from './csrf'

// findBy and waitFor wait for a visible condition; this is only their upper
// bound. The default (1 s) was too tight when the whole suite runs in
// parallel and a page chains requests (the group chip: logs, then the title).
configure({ asyncUtilTimeout: 5000 })

afterEach(() => {
  cleanup()
  // A mutation without Content-Type: application/json would be a 403 from the real API.
  const v = takeCsrfViolations()
  if (v.length > 0) throw new Error(`Requests the API would refuse (CSRF rule):${String.fromCharCode(10)}${v.join(String.fromCharCode(10))}`)
})
