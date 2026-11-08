import { Dialog } from './Dialog'
import { SHORTCUTS, type Shortcut } from '../shortcuts'

const GROUPS: Shortcut['where'][] = ['Anywhere', 'Log explorer', 'Live tail', 'Error groups']

/** Every shortcut, listed from the same definition the handlers use. */
export function ShortcutsHelp({ onClose }: { onClose: () => void }) {
  return (
    <Dialog title="Keyboard shortcuts" onClose={onClose}>
      {GROUPS.map((g) => (
        <section key={g} className="shortcut-group" aria-label={g}>
          <h3>{g}</h3>
          <dl className="shortcuts">
            {SHORTCUTS.filter((s) => s.where === g).map((s) => (
              <div key={s.id} data-shortcut={s.id}>
                <dt>
                  {s.keys.map((k, i) => (
                    <span key={k}>
                      {i > 0 && ' or '}
                      <kbd>{k}</kbd>
                    </span>
                  ))}
                </dt>
                <dd>{s.description}</dd>
              </div>
            ))}
          </dl>
        </section>
      ))}
      <p className="field-hint">Single-key shortcuts do nothing while you type in a text field.</p>
      <div className="dialog-actions">
        <button type="button" className="button button-primary" onClick={onClose}>
          Close
        </button>
      </div>
    </Dialog>
  )
}
