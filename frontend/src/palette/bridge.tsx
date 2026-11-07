import { createContext, useContext, useEffect } from 'react'
import type { Action, ExplorerContext } from './commands'

/** What the open explorer tells the palette: its state, and how to run its commands. */
export type ExplorerBridge = { ctx: ExplorerContext; run: (a: Action) => void }

export const BridgeContext = createContext<(b: ExplorerBridge | null) => void>(() => {})

/** Registers the explorer with the palette while the page is mounted. */
export function useExplorerBridge(bridge: ExplorerBridge) {
  const register = useContext(BridgeContext)
  useEffect(() => {
    register(bridge)
  })
  useEffect(() => () => register(null), [register])
}
