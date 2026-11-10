import { describe, expect, it } from 'vitest'
import appCss from '../styles/app.css?raw'

// jsdom has no layout: the incident pages' responsive rules are checked in the stylesheet.
const css = appCss.replace(/\r\n/g, '\n')

describe('incident layout', () => {
  it('two section columns that stack below 900 px, children allowed to shrink', () => {
    expect(css).toContain('.incident-sections { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);')
    expect(css).toContain('.incident-sections > section { min-width: 0; }')
    const narrow = css.slice(css.indexOf('@media (max-width: 900px) {\n  .incident-sections'))
    expect(narrow).toContain('.incident-sections { grid-template-columns: minmax(0, 1fr); }')
  })
  it('tables scroll inside their frame and long text wraps', () => {
    expect(css).toContain('.table-wrap { overflow-x: auto; }')
    expect(css).toContain('.comment-body {')
    expect(css.slice(css.indexOf('.comment-body {'))).toMatch(/^[^}]*white-space: pre-wrap; overflow-wrap: anywhere;/)
  })
  it('adds no animation (reduced motion needs nothing to turn off)', () => {
    const block = css.slice(css.indexOf('/* Incidents (L2'), css.indexOf('.project-nav a.nav-open,'))
    expect(block).not.toMatch(/animation|transition/)
  })
})
