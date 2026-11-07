import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import '@fontsource/red-hat-text/400.css'
import '@fontsource/red-hat-text/500.css'
import '@fontsource/red-hat-text/700.css'
import '@fontsource/red-hat-mono/400.css'
import '@fontsource/red-hat-mono/500.css'
import './styles/app.css'
import { App, newQueryClient } from './App'
import { ConfigContext, loadConfig } from './config'

const root = document.getElementById('root')
if (!root) throw new Error('missing #root')

// The runtime config comes first; it never blocks for long (short timeout, then defaults).
void loadConfig({ dev: import.meta.env.DEV }).then((config) =>
  createRoot(root).render(
    <StrictMode>
      <ConfigContext.Provider value={config}>
        <BrowserRouter>
          <App client={newQueryClient()} />
        </BrowserRouter>
      </ConfigContext.Provider>
    </StrictMode>,
  ),
)
