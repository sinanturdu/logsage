import { createContext, useContext, useEffect } from 'react'
import type { Action, ExplorerContext, GroupsContext, IncidentContext } from './commands'

/**
 * What the open page tells the palette: its state (the explorer's or the
 * error groups list's), and how to run its commands.
 */
export type ExplorerBridge = { ctx?: ExplorerContext; groups?: GroupsContext; incident?: IncidentContext | undefined; run: (a: Action) => void }

export const BridgeContext = createContext<(b: ExplorerBridge | null) => void>(() => {})

/** Registers the page with the palette while it is mounted. */
export function useExplorerBridge(bridge: ExplorerBridge) {
  const register = useContext(BridgeContext)
  useEffect(() => {
    register(bridge)
  })
  useEffect(() => () => register(null), [register])
}
