import { create } from 'zustand'
import api from '../lib/api'

// Module-level flag prevents concurrent refresh calls (survives re-renders).
// Using a plain variable instead of Zustand state avoids stale-closure bugs.
let refreshInProgress = false

// Timer ID for the proactive refresh scheduler. Stored at module level so
// it survives re-renders and can be cleared on logout.
let refreshTimer = null

// Schedule a proactive token refresh 13 minutes after a successful login/refresh.
// The access token lasts 15 minutes; refreshing at 13 min ensures the session
// never expires mid-use.
function scheduleRefresh() {
  if (refreshTimer) clearTimeout(refreshTimer)
  refreshTimer = setTimeout(() => {
    useAuthStore.getState().tryRefresh()
  }, 13 * 60 * 1000)
}

function cancelRefresh() {
  if (refreshTimer) {
    clearTimeout(refreshTimer)
    refreshTimer = null
  }
}

export const useAuthStore = create((set) => ({
  token: null,  // access token — kept in memory only, never persisted
  user:  null,
  ready: false, // true once the startup refresh attempt has completed

  login: async (username, password) => {
    const { data } = await api.post('/auth/login', { username, password })
    set({ token: data.token, user: parseJwt(data.token), ready: true })
    scheduleRefresh()
  },

  logout: async () => {
    cancelRefresh()
    try { await api.post('/auth/logout') } catch {}
    set({ token: null, user: null, ready: true })
  },

  // Called once on app startup to silently restore the session from the
  // httpOnly refresh-token cookie set by the server on login.
  // Also called by the proactive refresh timer every 13 minutes.
  tryRefresh: async () => {
    if (refreshInProgress) {
      // Another call is already in-flight; wait and then mark ready.
      // This handles React Strict Mode's double-effect invocation.
      return
    }
    refreshInProgress = true
    try {
      const { data } = await api.post('/auth/refresh')
      set({ token: data.token, user: parseJwt(data.token) })
      scheduleRefresh() // reschedule for the next 13-minute window
    } catch {
      // No valid cookie — user will be shown the login page.
      cancelRefresh()
    } finally {
      refreshInProgress = false
      set({ ready: true })
    }
  },
}))

function parseJwt(token) {
  try {
    return JSON.parse(atob(token.split('.')[1]))
  } catch {
    return null
  }
}
