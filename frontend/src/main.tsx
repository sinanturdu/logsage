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

const root = document.getElementById('root')
if (!root) throw new Error('missing #root')

createRoot(root).render(
  <StrictMode>
    <BrowserRouter>
      <App client={newQueryClient()} />
    </BrowserRouter>
  </StrictMode>,
)
