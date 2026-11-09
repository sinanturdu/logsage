import { useId } from 'react'

type Props = {
  name: string
  value: string
  checked: boolean
  label: string
  description: string
  onChange: () => void
  id?: string | undefined
}

/**
 * A radio option: the name on one line and the muted description on the
 * next. The name labels the radio and the description describes it, so a
 * screen reader says "Webhook, radio button, A signed JSON POST ..." rather
 * than one run-on sentence.
 */
export function Choice({ name, value, checked, label, description, onChange, id }: Props) {
  const uid = useId()
  return (
    <label className="choice">
      <input
        type="radio"
        name={name}
        id={id}
        value={value}
        checked={checked}
        aria-labelledby={`${uid}-name`}
        aria-describedby={`${uid}-desc`}
        onChange={onChange}
      />
      <span className="choice-text">
        <span className="choice-name" id={`${uid}-name`}>
          {label}
        </span>
        <span className="choice-desc" id={`${uid}-desc`}>
          {description}
        </span>
      </span>
    </label>
  )
}
