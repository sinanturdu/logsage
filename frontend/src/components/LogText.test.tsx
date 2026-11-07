import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { LogText } from './LogText'

const payloads = [
  '<img src=x onerror=alert(1)>',
  '<script>alert(1)</script>',
  '"><svg onload=alert(1)>',
  '<a href="javascript:alert(1)">click</a>',
]

describe('LogText', () => {
  it.each(payloads)('renders %s as text only', (payload) => {
    const { container } = render(<LogText text={payload} />)
    // No element other than the wrapper span was created...
    expect(container.querySelectorAll('*')).toHaveLength(1)
    expect(container.querySelector('img, script, svg, a')).toBeNull()
    // ...no event-handler or URL attribute exists anywhere...
    for (const el of container.querySelectorAll('*')) {
      for (const attr of el.getAttributeNames()) {
        expect(attr.startsWith('on') || attr === 'href' || attr === 'src').toBe(false)
      }
    }
    // ...and the text is exactly the input.
    expect(container.textContent).toBe(payload)
  })
})
