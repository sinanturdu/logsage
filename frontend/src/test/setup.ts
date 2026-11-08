import { afterEach } from 'vitest'
import { cleanup, configure } from '@testing-library/react'

// findBy and waitFor wait for a visible condition; this is only their upper
// bound. The default (1 s) was too tight when the whole suite runs in
// parallel and a page chains requests (the group chip: logs, then the title).
configure({ asyncUtilTimeout: 5000 })

afterEach(() => cleanup())
