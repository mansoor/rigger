import axios from 'axios'
import { useAuthStore } from '../store/auth'

const api = axios.create({ baseURL: '/api' })

// Attach JWT to every request
api.interceptors.request.use((config) => {
  const token = useAuthStore.getState().token
  if (token) config.headers.Authorization = `Bearer ${token}`
  return config
})

// On 401, clear auth and redirect to login.
// Exception: /auth/refresh — a 401 there just means no session to restore;
// let tryRefresh() handle it silently without triggering a redirect loop.
api.interceptors.response.use(
  (res) => res,
  (err) => {
    const isRefreshCall = err.config?.url?.includes('/auth/refresh')
    if (err.response?.status === 401 && !isRefreshCall) {
      useAuthStore.getState().logout()
      window.location.href = '/login'
    }
    return Promise.reject(err)
  }
)

export default api

// ── Workspace helpers ─────────────────────────────────────────────────────────

export const fetchTemplates    = ()          => api.get('/templates').then(r => r.data)
export const fetchTemplate     = (name)      => api.get(`/templates/${name}`).then(r => r.data)
export const recordTemplateUse = (name)      => api.post(`/templates/${name}/use`).then(r => r.data)
export const fetchWorkspaces   = ()          => api.get('/workspaces').then(r => r.data)
export const fetchWorkspace    = (name)      => api.get(`/workspaces/${name}`).then(r => r.data)
export const fetchEnvVars      = (name, env, reveal = false) => api.get(`/workspaces/${name}/envs/${env}/vars${reveal ? '?reveal=true' : ''}`).then(r => r.data)
export const fetchEnvStatus    = (name, env) => api.get(`/workspaces/${name}/envs/${env}/status`).then(r => r.data)
export const fetchImageUpdates  = (name, env) => api.get(`/workspaces/${name}/envs/${env}/image-updates`).then(r => r.data)
export const fetchContainers    = (name, env) => api.get(`/workspaces/${name}/envs/${env}/containers`).then(r => r.data)
export const fetchEnvMetrics    = (name, env, hours = 24) => api.get(`/workspaces/${name}/envs/${env}/metrics`, { params: { hours } }).then(r => r.data)
export const fetchActivity     = (name)      => api.get(`/workspaces/${name}/activity`).then(r => r.data)
export const fetchAllActivity  = ()          => api.get('/activity').then(r => r.data)
export const updateEnvVars     = (name, env, updates, deletes = []) =>
  api.patch(`/workspaces/${name}/envs/${env}/vars`, { updates, deletes }).then(r => r.data)
export const fetchCompose      = (name, env) => api.get(`/workspaces/${name}/envs/${env}/compose`).then(r => r.data)
export const putCompose        = (name, env, content) =>
  api.put(`/workspaces/${name}/envs/${env}/compose`, { content }).then(r => r.data)
export const fetchConfig       = (name)      => api.get(`/workspaces/${name}/config`).then(r => r.data)
export const changePassword    = (current_password, new_password) =>
  api.post('/auth/password', { current_password, new_password }).then(r => r.data)
export const fetchBackups      = ()          => api.get('/backups').then(r => r.data)
export const deleteBackup      = (workspace, env, date) => api.delete(`/backups/${workspace}/${env}/${date}`).then(r => r.data)
export const fetchStats        = ()          => api.get('/stats').then(r => r.data)
export const fetchLiveStats    = ()          => api.get('/live-stats').then(r => r.data)
export const exportTemplate    = (name, body) => api.post(`/workspaces/${name}/export-template`, body).then(r => r.data)
export const saveToolTemplate  = (name, content, force = false) =>
  api.post('/tools/save-template', { name, content, force }).then(r => r.data)

// ── Workspace backup / restore ────────────────────────────────────────────────
export const startWorkspaceBackup    = (workspace) =>
  api.post('/tools/workspace-backup', { workspace }).then(r => r.data)
export const getBackupJob            = (id) =>
  api.get(`/tools/backup-jobs/${id}`).then(r => r.data)
export const listWorkspaceArchives   = () =>
  api.get('/tools/workspace-archives').then(r => r.data)
export const deleteWorkspaceArchive  = (filename) =>
  api.delete(`/tools/workspace-archives/${filename}`).then(r => r.data)
export const restoreWorkspace = (formData) =>
  api.post('/tools/workspace-restore', formData, {
    headers: { 'Content-Type': 'multipart/form-data' },
  }).then(r => r.data)
export const deleteWorkspace   = (name)      => api.delete(`/workspaces/${name}`).then(r => r.data)
export const putConfig         = (name, content) =>
  api.put(`/workspaces/${name}/config`, { content }).then(r => r.data)

// ── Housekeeping ──────────────────────────────────────────────────────────────

export const fetchHousekeepingStatus     = ()       => api.get('/housekeeping/status').then(r => r.data)
export const fetchHousekeepingLog        = ()       => api.get('/housekeeping/log').then(r => r.data)
export const fetchHousekeepingImages     = ()       => api.get('/housekeeping/docker/images').then(r => r.data)
export const fetchStoppedContainers      = ()       => api.get('/housekeeping/docker/containers').then(r => r.data)
export const fetchDanglingVolumes        = ()       => api.get('/housekeeping/docker/volumes').then(r => r.data)
export const pruneDanglingImages         = ()       => api.post('/housekeeping/docker/prune/dangling-images').then(r => r.data)
export const pruneUnusedImages           = (body)   => api.post('/housekeeping/docker/prune/unused-images', body).then(r => r.data)
export const pruneContainers             = ()       => api.post('/housekeeping/docker/prune/containers').then(r => r.data)
export const pruneVolumes                = (body)   => api.post('/housekeeping/docker/prune/volumes', body).then(r => r.data)
export const pruneNetworks               = ()       => api.post('/housekeeping/docker/prune/networks').then(r => r.data)
export const pruneBuildCache             = ()       => api.post('/housekeeping/docker/prune/build-cache').then(r => r.data)
export const fetchJournalStats           = ()       => api.get('/housekeeping/host/journal/stats').then(r => r.data)
export const journalVacuum               = (body)   => api.post('/housekeeping/host/journal/vacuum', body).then(r => r.data)
export const fetchKernels                = ()       => api.get('/housekeeping/host/kernels').then(r => r.data)
export const cleanKernels                = (body)   => api.post('/housekeeping/host/kernels/clean', body).then(r => r.data)
export const aptClean                    = ()       => api.post('/housekeeping/host/apt/clean').then(r => r.data)
export const cleanTmp                    = (body)   => api.post('/housekeeping/host/tmp/clean', body).then(r => r.data)

// Migration leftovers (Phase 7) — data/files left on a source host after a migration.
export const fetchMigrationLeftovers  = ()   => api.get('/housekeeping/migration-leftovers').then(r => r.data)
export const dismissMigrationLeftover = (id) => api.delete(`/housekeeping/migration-leftovers/${id}`)
export const cleanMigrationLeftover   = (id, onChunk) =>
  streamText(`/api/housekeeping/migration-leftovers/${id}/clean`, 'POST', {}, onChunk)

// ── Settings: General ────────────────────────────────────────────────────────

export const fetchGeneralSettings  = ()     => api.get('/settings/general').then(r => r.data)
export const updateGeneralSettings = (body) => api.put('/settings/general', body).then(r => r.data)

// ── Settings: Backup Targets ──────────────────────────────────────────────────

export const fetchBackupTargets   = ()          => api.get('/settings/backup-targets').then(r => r.data)
export const createBackupTarget   = (body)      => api.post('/settings/backup-targets', body).then(r => r.data)
export const updateBackupTarget   = (id, body)  => api.put(`/settings/backup-targets/${id}`, body).then(r => r.data)
export const deleteBackupTarget   = (id)        => api.delete(`/settings/backup-targets/${id}`)

// ── Settings: Docker Registries ───────────────────────────────────────────────

export const fetchRegistries      = ()          => api.get('/settings/registries').then(r => r.data)
export const createRegistry       = (body)      => api.post('/settings/registries', body).then(r => r.data)
export const updateRegistry       = (id, body)  => api.put(`/settings/registries/${id}`, body).then(r => r.data)
export const deleteRegistry       = (id)        => api.delete(`/settings/registries/${id}`)
export const testRegistry         = (id)        => api.post(`/settings/registries/${id}/test`).then(r => r.data)

// ── Hosts (Phase 7: Multi-Host Support) ───────────────────────────────────────

export const fetchHosts = ()         => api.get('/hosts').then(r => r.data)
export const createHost = (body)     => api.post('/hosts', body).then(r => r.data)
export const updateHost = (id, body) => api.put(`/hosts/${id}`, body).then(r => r.data)
export const deleteHost = (id)       => api.delete(`/hosts/${id}`)
export const testHost   = (id)       => api.post(`/hosts/${id}/test`).then(r => r.data)
export const fetchManagedHostKey = () => api.get('/hosts/managed-key').then(r => r.data)
export const scanHost   = (id)       => api.post(`/hosts/${id}/scan`).then(r => r.data)
export const importHost = (id, workspaces) => api.post(`/hosts/${id}/import`, { workspaces }).then(r => r.data)
export const fetchHostStats = (id)   => api.get(`/hosts/${id}/stats`).then(r => r.data)

// Stream a chunked plain-text response body, invoking onChunk per chunk.
async function streamText(url, method, body, onChunk) {
  const token = useAuthStore.getState().token
  const res = await fetch(url, {
    method,
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    body: JSON.stringify(body),
  })
  if (!res.body) throw new Error('request failed')
  const reader = res.body.getReader()
  const dec = new TextDecoder()
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    onChunk(dec.decode(value, { stream: true }))
  }
}

// migrateWorkspace / setEnvHost start an async background job and return it
// ({ id, status, ... }). Poll getMigrationJob(id) for progress; completion also
// fires an in-app alert + notification channels.
export const migrateWorkspace = (name, targetHostId) =>
  api.post(`/workspaces/${name}/migrate`, { target_host_id: targetHostId }).then(r => r.data)
export const setEnvHost = (name, env, hostId) =>
  api.put(`/workspaces/${name}/envs/${env}/host`, { host_id: hostId }).then(r => r.data)
export const getMigrationJob = (id) => api.get(`/migration-jobs/${id}`).then(r => r.data)

// ── Settings: Notification Channels (Phase 6b) ────────────────────────────────

export const fetchNotificationChannels = ()         => api.get('/settings/notification-channels').then(r => r.data)
export const createNotificationChannel = (body)     => api.post('/settings/notification-channels', body).then(r => r.data)
export const updateNotificationChannel = (id, body) => api.put(`/settings/notification-channels/${id}`, body).then(r => r.data)
export const deleteNotificationChannel = (id)       => api.delete(`/settings/notification-channels/${id}`)
export const testNotificationChannel   = (id)       => api.post(`/settings/notification-channels/${id}/test`).then(r => r.data)

// ── Alerts (Phase 6) ──────────────────────────────────────────────────────────

export const fetchAlertMeta      = ()         => api.get('/alerts/meta').then(r => r.data)
export const fetchAlertSummary   = ()         => api.get('/alerts/summary').then(r => r.data)
export const fetchAlertRules     = ()         => api.get('/alerts/rules').then(r => r.data)
export const createAlertRule     = (body)     => api.post('/alerts/rules', body).then(r => r.data)
export const updateAlertRule     = (id, body) => api.put(`/alerts/rules/${id}`, body).then(r => r.data)
export const deleteAlertRule     = (id)       => api.delete(`/alerts/rules/${id}`)
export const fetchAlertEvents    = (opts = {}) => api.get('/alerts/events', { params: opts }).then(r => r.data)
export const fetchAlertUnread    = ()         => api.get('/alerts/events/unread-count').then(r => r.data)
export const dismissAlert        = (id)       => api.post(`/alerts/events/${id}/dismiss`).then(r => r.data)
export const dismissAllAlerts    = ()         => api.post('/alerts/events/dismiss-all').then(r => r.data)

// ── WebSocket action helper ───────────────────────────────────────────────────

export function openCreateSocket(workspace) {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
  const ws = new WebSocket(`${proto}://${window.location.host}/api/workspaces/create`)
  ws.addEventListener('open', () => {
    const token = useAuthStore.getState().token
    ws.send(JSON.stringify({ token, workspace }))
  })
  return ws
}

export function openActionSocket(name, command, env, extra = []) {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
  const ws = new WebSocket(`${proto}://${window.location.host}/api/workspaces/${name}/action`)

  ws.addEventListener('open', () => {
    const token = useAuthStore.getState().token
    ws.send(JSON.stringify({ command, env, extra, token }))
  })

  return ws
}
