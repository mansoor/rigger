import { create } from 'zustand'

// The currently-selected parent-tier Workspace. This scopes the whole UI: the
// sidebar shows only this workspace's projects, the dashboard filters to it, and
// the create-project wizard nests new projects under it.
//
// Persisted to localStorage so a reload keeps the same scope. A deep-link to
// /workspaces/:workspace/... overrides it (the route is authoritative); pages
// call setCurrent() on mount to keep the store in sync with the URL.

const STORAGE_KEY = 'rigger.workspace'

function initial() {
  try { return localStorage.getItem(STORAGE_KEY) || '' } catch { return '' }
}

export const useWorkspaceStore = create((set) => ({
  current: initial(),
  setCurrent: (name) => {
    try {
      if (name) localStorage.setItem(STORAGE_KEY, name)
      else localStorage.removeItem(STORAGE_KEY)
    } catch { /* ignore quota / privacy-mode errors */ }
    set({ current: name || '' })
  },
}))

// Non-hook accessor for use outside React (e.g. default-workspace fallbacks).
export const getCurrentWorkspace = () => useWorkspaceStore.getState().current
