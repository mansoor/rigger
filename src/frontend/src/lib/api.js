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
export const fetchContainerInspect = (name, env, svc) => api.get(`/workspaces/${name}/envs/${env}/containers/${svc}/inspect`).then(r => r.data)
export const fetchContainerStats   = (name, env, svc) => api.get(`/workspaces/${name}/envs/${env}/containers/${svc}/stats`).then(r => r.data)
export const fetchContainerTop     = (name, env, svc) => api.get(`/workspaces/${name}/envs/${env}/containers/${svc}/top`).then(r => r.data)
export const fetchContainerHistory = (name, env, svc) => api.get(`/workspaces/${name}/envs/${env}/containers/${svc}/image-history`).then(r => r.data)

// ── Container file browser (Wave D) ────────────────────────────────────────────
const filesBase = (name, env, svc) => `/workspaces/${name}/envs/${env}/containers/${svc}`
export const fetchContainerFiles = (name, env, svc, path = '/') =>
  api.get(`${filesBase(name, env, svc)}/files`, { params: { path } }).then(r => r.data)
export const fetchContainerFile  = (name, env, svc, path) =>
  api.get(`${filesBase(name, env, svc)}/file`, { params: { path } }).then(r => r.data)
export const saveContainerFile   = (name, env, svc, path, content) =>
  api.put(`${filesBase(name, env, svc)}/file`, content, {
    params: { path }, headers: { 'Content-Type': 'text/plain' },
  }).then(r => r.data)
export const deleteContainerFile = (name, env, svc, path) =>
  api.delete(`${filesBase(name, env, svc)}/file`, { params: { path } }).then(r => r.data)
export const renameContainerFile = (name, env, svc, path, to) =>
  api.post(`${filesBase(name, env, svc)}/file-rename`, null, { params: { path, to } }).then(r => r.data)
export const chmodContainerFile  = (name, env, svc, path, mode) =>
  api.post(`${filesBase(name, env, svc)}/file-chmod`, null, { params: { path, mode } }).then(r => r.data)
export const mkdirContainerDir   = (name, env, svc, path) =>
  api.post(`${filesBase(name, env, svc)}/file-mkdir`, null, { params: { path } }).then(r => r.data)
export const newContainerFile    = (name, env, svc, path) =>
  api.post(`${filesBase(name, env, svc)}/file-new`, null, { params: { path } }).then(r => r.data)
export const uploadContainerFile = (name, env, svc, dir, file) => {
  const form = new FormData()
  form.append('path', dir)
  form.append('file', file)
  return api.post(`${filesBase(name, env, svc)}/files-upload`, form).then(r => r.data)
}
// Fetch a file as a blob (carries the auth header) and trigger a browser download.
export const downloadContainerFile = async (name, env, svc, path) => {
  const res = await api.get(`${filesBase(name, env, svc)}/file-download`, {
    params: { path }, responseType: 'blob',
  })
  const url = URL.createObjectURL(res.data)
  const a = document.createElement('a')
  a.href = url
  a.download = path.split('/').pop() || 'download'
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}
export const fetchEnvMetrics    = (name, env, minutes = 60) => api.get(`/workspaces/${name}/envs/${env}/metrics`, { params: { minutes } }).then(r => r.data)
export const fetchMetricsConfig = ()         => api.get('/metrics/config').then(r => r.data)
export const fetchActivity     = (name)      => api.get(`/workspaces/${name}/activity`).then(r => r.data)
export const fetchActionRuns   = (name, limit = 100) => api.get(`/workspaces/${name}/action-runs`, { params: { limit } }).then(r => r.data)
export const clearActionRuns   = (name)      => api.delete(`/workspaces/${name}/action-runs`).then(r => r.data)
// Host-aware port-conflict check: host_ports = [{host_id, port, service}].
export const checkPorts        = (hostPorts, excludeWorkspace = '') =>
  api.post('/port-check', { host_ports: hostPorts, exclude_workspace: excludeWorkspace }).then(r => r.data)
export const fetchAllActivity  = ()          => api.get('/activity').then(r => r.data)
export const updateEnvVars     = (name, env, updates, deletes = [], secretKeys = []) =>
  api.patch(`/workspaces/${name}/envs/${env}/vars`, { updates, deletes, secret_keys: secretKeys }).then(r => r.data)
export const rotateSecret      = (name, env, key, newValue) =>
  api.post(`/workspaces/${name}/envs/${env}/rotate`, { key, new_value: newValue }).then(r => r.data)
export const fetchSecretEvents = (name, env) =>
  api.get(`/workspaces/${name}/envs/${env}/secret-events`).then(r => r.data)
export const fetchCompose      = (name, env) => api.get(`/workspaces/${name}/envs/${env}/compose`).then(r => r.data)
export const putCompose        = (name, env, content) =>
  api.put(`/workspaces/${name}/envs/${env}/compose`, { content }).then(r => r.data)
export const fetchConfig       = (name)      => api.get(`/workspaces/${name}/config`).then(r => r.data)
export const changePassword    = (current_password, new_password) =>
  api.post('/auth/password', { current_password, new_password }).then(r => r.data)
export const fetchBackups      = ()          => api.get('/backups').then(r => r.data)
export const fetchBackupCoverage = ()        => api.get('/backups/coverage').then(r => r.data) // 11b
export const deleteBackup      = (workspace, env, date) => api.delete(`/backups/${workspace}/${env}/${date}`).then(r => r.data)
export const fetchStats        = ()          => api.get('/stats').then(r => r.data)
export const fetchLiveStats    = ()          => api.get('/live-stats').then(r => r.data)
// Generate a prebuilt-template JSON draft from an image workspace (no file
// written) — loaded into the Template Manager editor for review and save.
export const fetchTemplateDraft = (name, env) =>
  api.get(`/workspaces/${name}/template-draft${env ? `?env=${encodeURIComponent(env)}` : ''}`).then(r => r.data)
export const saveToolTemplate  = (name, content, force = false) =>
  api.post('/tools/save-template', { name, content, force }).then(r => r.data)

// ── Workspace backup / restore ────────────────────────────────────────────────
export const startWorkspaceBackup    = (workspace, name) =>
  api.post('/tools/workspace-backup', { workspace, name }).then(r => r.data)
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
// Restore directly from a backup already stored on the server (no re-upload).
export const restoreWorkspaceFromArchive = (filename, force = false) =>
  api.post(`/tools/workspace-archives/${encodeURIComponent(filename)}/restore`, { force }).then(r => r.data)
// Upload a .rwb backup to the server (stored, then restored from the list).
export const uploadWorkspaceArchive = (formData) =>
  api.post('/tools/workspace-archives/upload', formData, {
    headers: { 'Content-Type': 'multipart/form-data' },
  }).then(r => r.data)
// ── Workspace configuration snapshots (.rws — config only, no data) ──
export const createWorkspaceSnapshot = (workspace, name) =>
  api.post('/tools/workspace-snapshots', { workspace, name }).then(r => r.data)
export const fetchWorkspaceSnapshots = () =>
  api.get('/tools/workspace-snapshots').then(r => r.data)
export const deleteWorkspaceSnapshot = (filename) =>
  api.delete(`/tools/workspace-snapshots/${encodeURIComponent(filename)}`).then(r => r.data)
export const rollbackWorkspaceSnapshot = (filename) =>
  api.post(`/tools/workspace-snapshots/${encodeURIComponent(filename)}/rollback`).then(r => r.data)
export const uploadWorkspaceSnapshot = (formData) =>
  api.post('/tools/workspace-snapshots/upload', formData, {
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

// Appearance prefs are persisted as a JSON blob under the general-settings
// `appearance_prefs` key (cross-device sync; localStorage is the local cache).
export const fetchAppearancePrefs = () =>
  api.get('/settings/general').then(r => {
    try { return r.data?.appearance_prefs ? JSON.parse(r.data.appearance_prefs) : null }
    catch { return null }
  })
export const saveAppearancePrefs = (prefs) =>
  api.put('/settings/general', { appearance_prefs: JSON.stringify(prefs) }).then(r => r.data)

// ── Settings: Backup Targets ──────────────────────────────────────────────────

export const fetchBackupTargets   = ()          => api.get('/settings/backup-targets').then(r => r.data)
export const createBackupTarget   = (body)      => api.post('/settings/backup-targets', body).then(r => r.data)
export const updateBackupTarget   = (id, body)  => api.put(`/settings/backup-targets/${id}`, body).then(r => r.data)
export const deleteBackupTarget   = (id)        => api.delete(`/settings/backup-targets/${id}`)
export const testBackupTarget     = (id)        => api.post(`/settings/backup-targets/${id}/test`).then(r => r.data)

// 11d: push the latest local snapshot of an env to its configured remote target.
export const syncEnvBackup        = (name, env, body = {}) =>
  api.post(`/workspaces/${name}/envs/${env}/backup-sync`, body).then(r => r.data)

// 11c: non-destructive dry-run check that a snapshot is complete + restorable.
export const verifyRestore        = (name, env, date) =>
  api.post(`/workspaces/${name}/envs/${env}/restore-verify`, { date }).then(r => r.data)

// 11e: per-env snapshot stats (count, total size, oldest/newest, schedules).
export const fetchBackupStats     = (name, env) =>
  api.get(`/workspaces/${name}/envs/${env}/backup-stats`).then(r => r.data)

// Per-env data-bearing services for the backup pickers (Phase 11 per-env).
export const fetchBackupServices  = (name, env) =>
  api.get(`/workspaces/${name}/envs/${env}/backup-services`).then(r => r.data)

// 11a: push a full .rwb workspace archive to a remote target.
export const syncWorkspaceArchive = (filename, body = {}) =>
  api.post(`/tools/workspace-archives/${encodeURIComponent(filename)}/sync`, body).then(r => r.data)

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

export function openActionSocket(name, command, env, extra = [], services = []) {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
  const ws = new WebSocket(`${proto}://${window.location.host}/api/workspaces/${name}/action`)

  ws.addEventListener('open', () => {
    const token = useAuthStore.getState().token
    ws.send(JSON.stringify({ command, env, extra, services, token }))
  })

  return ws
}
