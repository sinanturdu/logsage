import { describe, expect, it } from 'vitest'
import appCss from '../styles/app.css?raw'

// jsdom has no layout, so the rule form's layout is checked in the stylesheet.
const css = appCss.replace(/\r\n/g, '\n')
/** The declarations of the last rule with exactly this selector. */
function rule(selector: string): string {
  const at = css.lastIndexOf(`\n${selector} {`)
  expect(at, selector).toBeGreaterThan(-1)
  return css.slice(css.indexOf('{', at) + 1, css.indexOf('}', at)).trim()
}

describe('rule form layout', () => {
  it('wraps the field rows and lets grid children shrink', () => {
    expect(rule('.field-row')).toContain('grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr))')
    expect(rule('.field-row > *')).toContain('min-width: 0')
  })

  it('keeps the number and unit as one compact group that beats the generic input width', () => {
    const generic = ".dialog input:not([type='checkbox']):not([type='radio']), .dialog select"
    const number = ".dialog .duration input:not([type='checkbox']):not([type='radio'])"
    expect(rule(number)).toContain('width: 5rem')
    expect(rule('.dialog .duration select')).toContain('width: 7.5rem')
    // Same specificity as the generic rule, so it must come after it.
    expect(css.lastIndexOf(`\n${number} {`)).toBeGreaterThan(css.indexOf(generic))
    // Two compact controls fit in the narrowest column (14rem) with the gap.
    expect(5 + 7.5 + 0.25).toBeLessThanOrEqual(14)
  })

  it('never scrolls the dialog sideways', () => {
    const d = rule('.dialog.rule-dialog')
    expect(d).toContain('overflow-x: hidden')
    expect(d).toContain('overflow-y: auto')
  })
})
