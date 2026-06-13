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

// ── Workspace (tier) + Project helpers ─────────────────────────────────────────
//
// Hierarchy: Workspace (tier) → Project → Environment. Project/env functions take
// a leading `ws` (the parent workspace) plus the project `name`, and build the
// nested URL /workspaces/{ws}/projects/{name}/...

const projBase = (ws, name) => `/workspaces/${ws}/projects/${name}`

export const fetchTemplates    = ()          => api.get('/templates').then(r => r.data)
export const fetchTemplate     = (name)      => api.get(`/templates/${name}`).then(r => r.data)
export const recordTemplateUse = (name)      => api.post(`/templates/${name}/use`).then(r => r.data)

// Workspace tier: list / create / delete the parent-tier workspaces.
export const fetchWorkspaces      = ()      => api.get('/workspaces').then(r => r.data)
export const createWorkspaceTier  = (name, key) => api.post('/workspaces', { name, key }).then(r => r.data)
export const renameWorkspaceTier  = (ws, name) => api.put(`/workspaces/${ws}`, { name }).then(r => r.data)
export const deleteWorkspaceTier  = (ws)    => api.delete(`/workspaces/${ws}`).then(r => r.data)
// Move projects to another workspace (projects omitted/empty = all; empties source).
export const transferWorkspace    = (ws, target, projects = []) =>
  api.post(`/workspaces/${ws}/transfer`, { target, projects }).then(r => r.data)

// Key helpers (short identifier for folders/URLs/Docker). suggestKey returns a
// derived, collision-free, validated key; checkKey validates a user override.
// type = 'workspace' | 'project'; project checks require the parent workspace key.
export const suggestKey = (type, name, workspace = '') =>
  api.get('/keys/suggest', { params: { type, name, workspace } }).then(r => r.data)
export const checkKey   = (type, key, workspace = '') =>
  api.get('/keys/check', { params: { type, key, workspace } }).then(r => r.data)
// Projects within a workspace.
export const fetchProjects        = (ws)    => api.get(`/workspaces/${ws}/projects`).then(r => r.data)
export const fetchWorkspace    = (ws, name)      => api.get(projBase(ws, name)).then(r => r.data)
export const fetchEnvVars      = (ws, name, env, reveal = false) => api.get(`${projBase(ws, name)}/envs/${env}/vars${reveal ? '?reveal=true' : ''}`).then(r => r.data)
// Managed-database connection info for an env (Phase 5). reveal=true (operator+) returns the password.
export const fetchDatabaseInfo = (ws, name, env, reveal = false) => api.get(`${projBase(ws, name)}/envs/${env}/database${reveal ? '?reveal=true' : ''}`).then(r => r.data)
// Phase 6 — safe DB management: list schemas/databases (+ table count/size) and create one.
export const fetchDatabaseSchemas = (ws, name, env) => api.get(`${projBase(ws, name)}/envs/${env}/database/schemas`).then(r => r.data)
export const createDatabaseSchema = (ws, name, env, schemaName) => api.post(`${projBase(ws, name)}/envs/${env}/database/schemas`, { name: schemaName }).then(r => r.data)
export const fetchEnvStatus    = (ws, name, env) => api.get(`${projBase(ws, name)}/envs/${env}/status`).then(r => r.data)
export const fetchImageUpdates  = (ws, name, env) => api.get(`${projBase(ws, name)}/envs/${env}/image-updates`).then(r => r.data)
export const fetchContainers    = (ws, name, env) => api.get(`${projBase(ws, name)}/envs/${env}/containers`).then(r => r.data)
export const fetchContainerInspect = (ws, name, env, svc) => api.get(`${projBase(ws, name)}/envs/${env}/containers/${svc}/inspect`).then(r => r.data)
export const fetchContainerStats   = (ws, name, env, svc) => api.get(`${projBase(ws, name)}/envs/${env}/containers/${svc}/stats`).then(r => r.data)
export const fetchContainerTop     = (ws, name, env, svc) => api.get(`${projBase(ws, name)}/envs/${env}/containers/${svc}/top`).then(r => r.data)
export const fetchContainerHistory = (ws, name, env, svc) => api.get(`${projBase(ws, name)}/envs/${env}/containers/${svc}/image-history`).then(r => r.data)

// ── Container file browser (Wave D) ────────────────────────────────────────────
const filesBase = (ws, name, env, svc) => `${projBase(ws, name)}/envs/${env}/containers/${svc}`
export const fetchContainerFiles = (ws, name, env, svc, path = '/') =>
  api.get(`${filesBase(ws, name, env, svc)}/files`, { params: { path } }).then(r => r.data)
export const fetchContainerFile  = (ws, name, env, svc, path) =>
  api.get(`${filesBase(ws, name, env, svc)}/file`, { params: { path } }).then(r => r.data)
export const saveContainerFile   = (ws, name, env, svc, path, content) =>
  api.put(`${filesBase(ws, name, env, svc)}/file`, content, {
    params: { path }, headers: { 'Content-Type': 'text/plain' },
  }).then(r => r.data)
export const deleteContainerFile = (ws, name, env, svc, path) =>
  api.delete(`${filesBase(ws, name, env, svc)}/file`, { params: { path } }).then(r => r.data)
export const renameContainerFile = (ws, name, env, svc, path, to) =>
  api.post(`${filesBase(ws, name, env, svc)}/file-rename`, null, { params: { path, to } }).then(r => r.data)
export const chmodContainerFile  = (ws, name, env, svc, path, mode) =>
  api.post(`${filesBase(ws, name, env, svc)}/file-chmod`, null, { params: { path, mode } }).then(r => r.data)
export const mkdirContainerDir   = (ws, name, env, svc, path) =>
  api.post(`${filesBase(ws, name, env, svc)}/file-mkdir`, null, { params: { path } }).then(r => r.data)
export const newContainerFile    = (ws, name, env, svc, path) =>
  api.post(`${filesBase(ws, name, env, svc)}/file-new`, null, { params: { path } }).then(r => r.data)
export const uploadContainerFile = (ws, name, env, svc, dir, file) => {
  const form = new FormData()
  form.append('path', dir)
  form.append('file', file)
  return api.post(`${filesBase(ws, name, env, svc)}/files-upload`, form).then(r => r.data)
}
// Fetch a file as a blob (carries the auth header) and trigger a browser download.
export const downloadContainerFile = async (ws, name, env, svc, path) => {
  const res = await api.get(`${filesBase(ws, name, env, svc)}/file-download`, {
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
export const fetchEnvMetrics    = (ws, name, env, minutes = 60) => api.get(`${projBase(ws, name)}/envs/${env}/metrics`, { params: { minutes } }).then(r => r.data)
export const fetchMetricsConfig = ()         => api.get('/metrics/config').then(r => r.data)
export const fetchActivity     = (ws, name)      => api.get(`${projBase(ws, name)}/activity`).then(r => r.data)
export const fetchDeployHistory = (ws, name, env)        => api.get(`${projBase(ws, name)}/envs/${env}/deploy-history`).then(r => r.data)
export const rollbackEnv        = (ws, name, env, toId)  => api.post(`${projBase(ws, name)}/envs/${env}/rollback`, { to_id: toId }).then(r => r.data)
export const fetchImageStatus   = (ws, name, env)        => api.get(`${projBase(ws, name)}/envs/${env}/image-status`).then(r => r.data)
export const trackLatest        = (ws, name, env)        => api.post(`${projBase(ws, name)}/envs/${env}/track-latest`).then(r => r.data)
export const fetchActionRuns   = (ws, name, limit = 100) => api.get(`${projBase(ws, name)}/action-runs`, { params: { limit } }).then(r => r.data)
export const clearActionRuns   = (ws, name)      => api.delete(`${projBase(ws, name)}/action-runs`).then(r => r.data)
// Release pipeline #4: explicit env deploy-tier order.
export const fetchEnvOrder     = (ws, name)        => api.get(`${projBase(ws, name)}/env-order`).then(r => r.data)
export const putEnvOrder       = (ws, name, order) => api.put(`${projBase(ws, name)}/env-order`, { order }).then(r => r.data)
export const setBuildPipeline  = (ws, name, id)    => api.put(`${projBase(ws, name)}/build-pipeline`, { pipeline_id: id || 0 }).then(r => r.data)
// Phase 9: deployment pipelines (project-scoped).
export const fetchPipelines    = (ws, name)           => api.get(`${projBase(ws, name)}/pipelines`).then(r => r.data)
export const createPipeline    = (ws, name, body)     => api.post(`${projBase(ws, name)}/pipelines`, body).then(r => r.data)
export const suggestPipeline   = (ws, name, opts)     => api.post(`${projBase(ws, name)}/pipelines/suggest`, opts).then(r => r.data)
export const updatePipeline    = (ws, name, id, body) => api.put(`${projBase(ws, name)}/pipelines/${id}`, body).then(r => r.data)
export const deletePipeline    = (ws, name, id)       => api.delete(`${projBase(ws, name)}/pipelines/${id}`).then(r => r.data)
// Trigger a run in the background; returns { run_id } so the UI can open a
// poll-based log/status view without holding a socket open.
export const startPipelineRun = (ws, name, id) => api.post(`${projBase(ws, name)}/pipelines/${id}/run`).then(r => r.data)
export const fetchPipelineRuns = (ws, name, id, limit = 30) => api.get(`${projBase(ws, name)}/pipelines/${id}/runs`, { params: { limit } }).then(r => r.data)
export const fetchPipelineRun  = (ws, name, id, runId) => api.get(`${projBase(ws, name)}/pipelines/${id}/runs/${runId}`).then(r => r.data)
export const approvePipelineRun = (ws, name, id, runId) => api.post(`${projBase(ws, name)}/pipelines/${id}/runs/${runId}/approve`).then(r => r.data)
export const rejectPipelineRun  = (ws, name, id, runId) => api.post(`${projBase(ws, name)}/pipelines/${id}/runs/${runId}/reject`).then(r => r.data)
export const fetchPipelineWebhooks = (ws, name, id) => api.get(`${projBase(ws, name)}/pipelines/${id}/webhooks`).then(r => r.data)
export const createPipelineWebhook = (ws, name, id, body = {}) => api.post(`${projBase(ws, name)}/pipelines/${id}/webhooks`, body).then(r => r.data)
export const deletePipelineWebhook = (ws, name, id, whId) => api.delete(`${projBase(ws, name)}/pipelines/${id}/webhooks/${whId}`).then(r => r.data)
// Host-aware port-conflict check: host_ports = [{host_id, port, service}].
export const checkPorts        = (hostPorts, excludeWorkspace = '') =>
  api.post('/port-check', { host_ports: hostPorts, exclude_workspace: excludeWorkspace }).then(r => r.data)
export const fetchAllActivity  = ()          => api.get('/activity').then(r => r.data)
export const updateEnvVars     = (ws, name, env, updates, deletes = [], secretKeys = []) =>
  api.patch(`${projBase(ws, name)}/envs/${env}/vars`, { updates, deletes, secret_keys: secretKeys }).then(r => r.data)
export const rotateSecret      = (ws, name, env, key, newValue) =>
  api.post(`${projBase(ws, name)}/envs/${env}/rotate`, { key, new_value: newValue }).then(r => r.data)
export const fetchSecretEvents = (ws, name, env) =>
  api.get(`${projBase(ws, name)}/envs/${env}/secret-events`).then(r => r.data)
export const fetchCompose      = (ws, name, env) => api.get(`${projBase(ws, name)}/envs/${env}/compose`).then(r => r.data)
export const putCompose        = (ws, name, env, content) =>
  api.put(`${projBase(ws, name)}/envs/${env}/compose`, { content }).then(r => r.data)
export const fetchConfig       = (ws, name)      => api.get(`${projBase(ws, name)}/config`).then(r => r.data)
export const changePassword    = (current_password, new_password) =>
  api.post('/auth/password', { current_password, new_password }).then(r => r.data)
// Optional workspace scoping: when a workspace key is passed these lists are
// filtered server-side to that workspace; omit it to span every workspace.
const wsQuery = (ws) => (ws ? `?workspace=${encodeURIComponent(ws)}` : '')
export const fetchBackups      = (ws)        => api.get(`/backups${wsQuery(ws)}`).then(r => r.data)
export const fetchBackupCoverage = (ws)      => api.get(`/backups/coverage${wsQuery(ws)}`).then(r => r.data) // 11b
export const deleteBackup      = (workspace, project, env, date) => api.delete(`/backups/${workspace}/${project}/${env}/${date}`).then(r => r.data)
export const fetchStats        = ()          => api.get('/stats').then(r => r.data)
export const fetchLiveStats    = ()          => api.get('/live-stats').then(r => r.data)
// Generate a prebuilt-template JSON draft from an image workspace (no file
// written) — loaded into the Template Manager editor for review and save.
export const fetchTemplateDraft = (ws, name, env) =>
  api.get(`${projBase(ws, name)}/template-draft${env ? `?env=${encodeURIComponent(env)}` : ''}`).then(r => r.data)
export const saveToolTemplate  = (name, content, force = false) =>
  api.post('/tools/save-template', { name, content, force }).then(r => r.data)
// Repo scanner (Phase 2b): clone + statically detect a stack into a draft service graph.
export const scanRepo          = (repo, branch) =>
  api.post('/scan-repo', { repo, branch }).then(r => r.data)
// Stack blueprints for the no-repo "start from a template" picker (incl. seeded services[]).
export const fetchBlueprints   = () => api.get('/blueprints').then(r => r.data)
// Managed database catalog (engines + selectable versions) for the DB picker.
export const fetchDatabases    = () => api.get('/databases').then(r => r.data)

// ── Workspace backup / restore ────────────────────────────────────────────────
export const startWorkspaceBackup    = (workspace, project, name) =>
  api.post('/tools/workspace-backup', { workspace, project, name }).then(r => r.data)
export const getBackupJob            = (id) =>
  api.get(`/tools/backup-jobs/${id}`).then(r => r.data)
export const listWorkspaceArchives   = (ws) =>
  api.get(`/tools/workspace-archives${wsQuery(ws)}`).then(r => r.data)
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
export const createWorkspaceSnapshot = (workspace, project, name) =>
  api.post('/tools/workspace-snapshots', { workspace, project, name }).then(r => r.data)
export const fetchWorkspaceSnapshots = (ws) =>
  api.get(`/tools/workspace-snapshots${wsQuery(ws)}`).then(r => r.data)
export const deleteWorkspaceSnapshot = (filename) =>
  api.delete(`/tools/workspace-snapshots/${encodeURIComponent(filename)}`).then(r => r.data)
export const rollbackWorkspaceSnapshot = (filename) =>
  api.post(`/tools/workspace-snapshots/${encodeURIComponent(filename)}/rollback`).then(r => r.data)
export const uploadWorkspaceSnapshot = (formData) =>
  api.post('/tools/workspace-snapshots/upload', formData, {
    headers: { 'Content-Type': 'multipart/form-data' },
  }).then(r => r.data)
export const deleteWorkspace   = (ws, name)      => api.delete(projBase(ws, name)).then(r => r.data)
export const putConfig         = (ws, name, content) =>
  api.put(`${projBase(ws, name)}/config`, { content }).then(r => r.data)

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

// Users (Phase 5 RBAC) — admin only. createUser now invites (email+role); returns
// { user, invite_link? } (link surfaced when system SMTP isn't configured).
export const fetchUsers   = ()         => api.get('/users').then(r => r.data)
export const inviteUser   = (body)     => api.post('/users', body).then(r => r.data)
export const updateUser   = (id, body) => api.put(`/users/${id}`, body).then(r => r.data)
export const deleteUser   = (id)       => api.delete(`/users/${id}`)
export const resendInvite = (id)       => api.post(`/users/${id}/resend-invite`).then(r => r.data)

// Access requests (roadmap 9).
export const fetchAccessTargets        = ()     => api.get('/access-requests/targets').then(r => r.data)
export const createAccessRequest       = (body) => api.post('/access-requests', body).then(r => r.data)
export const fetchMyAccessRequests     = ()     => api.get('/access-requests/mine').then(r => r.data)
export const fetchPendingAccessRequests = ()    => api.get('/access-requests').then(r => r.data)
export const approveAccessRequest      = (id)   => api.post(`/access-requests/${id}/approve`).then(r => r.data)
export const rejectAccessRequest       = (id)   => api.post(`/access-requests/${id}/reject`).then(r => r.data)

// Invite registration + email verification + profile (Phase 5.1b).
export const fetchRegisterInfo   = (token)       => api.get('/register/info', { params: { token } }).then(r => r.data)
export const completeRegistration = (body)       => api.post('/register/complete', body).then(r => r.data)
export const verifyEmail         = (token)       => api.post('/auth/verify-email', { token }).then(r => r.data)
export const resendVerification  = ()            => api.post('/auth/resend-verification').then(r => r.data)
export const updateProfile       = (body)        => api.put('/auth/profile', body).then(r => r.data)

// System (transactional) email settings — admin.
export const fetchSystemEmail  = ()     => api.get('/settings/system-email').then(r => r.data)
export const updateSystemEmail = (body) => api.put('/settings/system-email', body).then(r => r.data)

export const fetchGeneralSettings  = ()     => api.get('/settings/general').then(r => r.data)
export const updateGeneralSettings = (body) => api.put('/settings/general', body).then(r => r.data)

// Appearance prefs (W7): per-user, resolved server-side as
// user-override ?? workspace-default ?? global-default. localStorage is the
// local cache for instant/no-FOUC paint. `ws` scopes the workspace-default layer.
export const fetchAppearancePrefs = (ws) =>
  api.get('/auth/appearance', { params: ws ? { ws } : {} }).then(r => {
    try { return r.data?.appearance_prefs ? JSON.parse(r.data.appearance_prefs) : null }
    catch { return null }
  })
export const saveAppearancePrefs = (prefs) =>
  api.put('/auth/appearance', { appearance_prefs: JSON.stringify(prefs) }).then(r => r.data)
// Workspace default appearance (admins) — stored in workspace settings.
export const saveWorkspaceAppearance = (ws, prefs) =>
  api.put(`/workspaces/${ws}/settings`, { appearance_prefs: prefs ? JSON.stringify(prefs) : '' }).then(r => r.data)
// Global default appearance (super-admin) — stored in general settings.
export const saveGlobalAppearance = (prefs) =>
  api.put('/settings/general', { appearance_prefs: prefs ? JSON.stringify(prefs) : '' }).then(r => r.data)

// ── Settings: Backup Targets ──────────────────────────────────────────────────

// Admin (global Settings) view — every target; global ones carry their `grants`.
export const fetchBackupTargets   = ()          => api.get('/settings/backup-targets').then(r => r.data)
export const createBackupTarget   = (body)      => api.post('/settings/backup-targets', body).then(r => r.data)
export const updateBackupTarget   = (id, body)  => api.put(`/settings/backup-targets/${id}`, body).then(r => r.data)
export const deleteBackupTarget   = (id)        => api.delete(`/settings/backup-targets/${id}`)
export const testBackupTarget     = (id)        => api.post(`/settings/backup-targets/${id}/test`).then(r => r.data)

// Workspace-scoped backup-target pool (Phase 3): own targets + granted globals.
export const fetchWorkspaceBackupTargets = (ws)       => api.get(`/workspaces/${ws}/backup-targets`).then(r => r.data)
export const createWorkspaceBackupTarget = (ws, body) => api.post(`/workspaces/${ws}/backup-targets`, body).then(r => r.data)
export const updateWorkspaceBackupTarget = (ws, id, body) => api.put(`/workspaces/${ws}/backup-targets/${id}`, body).then(r => r.data)
export const deleteWorkspaceBackupTarget = (ws, id)   => api.delete(`/workspaces/${ws}/backup-targets/${id}`)
export const testWorkspaceBackupTarget   = (ws, id)   => api.post(`/workspaces/${ws}/backup-targets/${id}/test`).then(r => r.data)

// 11d: push the latest local snapshot of an env to its configured remote target.
export const syncEnvBackup        = (ws, name, env, body = {}) =>
  api.post(`${projBase(ws, name)}/envs/${env}/backup-sync`, body).then(r => r.data)

// 11c: non-destructive dry-run check that a snapshot is complete + restorable.
export const verifyRestore        = (ws, name, env, date) =>
  api.post(`${projBase(ws, name)}/envs/${env}/restore-verify`, { date }).then(r => r.data)

// 11e: per-env snapshot stats (count, total size, oldest/newest, schedules).
export const fetchBackupStats     = (ws, name, env) =>
  api.get(`${projBase(ws, name)}/envs/${env}/backup-stats`).then(r => r.data)

// Per-env data-bearing services for the backup pickers (Phase 11 per-env).
export const fetchBackupServices  = (ws, name, env) =>
  api.get(`${projBase(ws, name)}/envs/${env}/backup-services`).then(r => r.data)

// 11a: push a full .rwb workspace archive to a remote target.
export const syncWorkspaceArchive = (filename, body = {}) =>
  api.post(`/tools/workspace-archives/${encodeURIComponent(filename)}/sync`, body).then(r => r.data)

// ── Settings: Docker Registries ───────────────────────────────────────────────

// Admin (global Settings) view — every registry; global ones carry their `grants`.
export const fetchRegistries      = ()          => api.get('/settings/registries').then(r => r.data)
export const createRegistry       = (body)      => api.post('/settings/registries', body).then(r => r.data)
export const updateRegistry       = (id, body)  => api.put(`/settings/registries/${id}`, body).then(r => r.data)
export const deleteRegistry       = (id)        => api.delete(`/settings/registries/${id}`)
export const testRegistry         = (id)        => api.post(`/settings/registries/${id}/test`).then(r => r.data)

// Workspace-scoped registry pool (Phase 3): own registries + granted globals.
export const fetchWorkspaceRegistries = (ws)         => api.get(`/workspaces/${ws}/registries`).then(r => r.data)
export const createWorkspaceRegistry  = (ws, body)   => api.post(`/workspaces/${ws}/registries`, body).then(r => r.data)
export const updateWorkspaceRegistry  = (ws, id, body) => api.put(`/workspaces/${ws}/registries/${id}`, body).then(r => r.data)
export const deleteWorkspaceRegistry  = (ws, id)     => api.delete(`/workspaces/${ws}/registries/${id}`)
export const testWorkspaceRegistry    = (ws, id)     => api.post(`/workspaces/${ws}/registries/${id}/test`).then(r => r.data)
// Test ad-hoc credentials before they're saved (project registry picker).
export const testRegistryCredentials  = (ws, body)   => api.post(`/workspaces/${ws}/registries/test-credentials`, body).then(r => r.data)

// Workspace membership + per-project overrides (Phase 5.2).
export const fetchWorkspaceMembers = (ws)              => api.get(`/workspaces/${ws}/members`).then(r => r.data)
export const fetchMemberCandidates = (ws)              => api.get(`/workspaces/${ws}/members/candidates`).then(r => r.data)
export const setWorkspaceMember    = (ws, uid, role)  => api.put(`/workspaces/${ws}/members/${uid}`, { role }).then(r => r.data)
export const removeWorkspaceMember = (ws, uid)        => api.delete(`/workspaces/${ws}/members/${uid}`)
export const setProjectOverride    = (ws, uid, proj, role) => api.put(`/workspaces/${ws}/members/${uid}/projects/${proj}`, { role }).then(r => r.data)
export const removeProjectOverride = (ws, uid, proj)  => api.delete(`/workspaces/${ws}/members/${uid}/projects/${proj}`)

// Per-workspace general settings (Phase 3): acme_email, domain.
export const fetchWorkspaceSettings = (ws)       => api.get(`/workspaces/${ws}/settings`).then(r => r.data)
export const updateWorkspaceSettings = (ws, body) => api.put(`/workspaces/${ws}/settings`, body).then(r => r.data)

// ── Hosts (Phase 7: Multi-Host Support) ───────────────────────────────────────

// Admin (global Settings) view — every host across all scopes; global hosts
// carry their `grants` allowlist ('*' = offered to all workspaces).
export const fetchHosts = ()         => api.get('/hosts').then(r => r.data)
export const createHost = (body)     => api.post('/hosts', body).then(r => r.data)
export const updateHost = (id, body) => api.put(`/hosts/${id}`, body).then(r => r.data)
export const deleteHost = (id)       => api.delete(`/hosts/${id}`)
export const testHost   = (id)       => api.post(`/hosts/${id}/test`).then(r => r.data)
export const fetchManagedHostKey = () => api.get('/hosts/managed-key').then(r => r.data)
export const scanHost   = (id)       => api.post(`/hosts/${id}/scan`).then(r => r.data)
export const importHost = (id, workspaces) => api.post(`/hosts/${id}/import`, { workspaces }).then(r => r.data)
export const fetchHostStats = (id)   => api.get(`/hosts/${id}/stats`).then(r => r.data)

// Workspace-scoped host pool (Phase 3): a workspace's own hosts + globals granted
// to it. owner_scope is 'global' or 'ws:{key}'. Create/edit/delete are allowed for
// own hosts only; globals are read-only here (managed from admin Settings).
export const fetchWorkspaceHosts  = (ws)         => api.get(`/workspaces/${ws}/hosts`).then(r => r.data)
export const createWorkspaceHost  = (ws, body)   => api.post(`/workspaces/${ws}/hosts`, body).then(r => r.data)
export const updateWorkspaceHost  = (ws, id, body) => api.put(`/workspaces/${ws}/hosts/${id}`, body).then(r => r.data)
export const deleteWorkspaceHost  = (ws, id)     => api.delete(`/workspaces/${ws}/hosts/${id}`)
export const testWorkspaceHost    = (ws, id)     => api.post(`/workspaces/${ws}/hosts/${id}/test`).then(r => r.data)
export const fetchWorkspaceHostStats = (ws, id)  => api.get(`/workspaces/${ws}/hosts/${id}/stats`).then(r => r.data)

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
export const migrateWorkspace = (ws, name, targetHostId) =>
  api.post(`${projBase(ws, name)}/migrate`, { target_host_id: targetHostId }).then(r => r.data)
// bindOnly=true records the binding without a migration job — for new, not-yet-
// deployed environments (Edit Project). Otherwise it migrates a running env.
export const setEnvHost = (ws, name, env, hostId, bindOnly = false) =>
  api.put(`${projBase(ws, name)}/envs/${env}/host`, { host_id: hostId, bind_only: bindOnly }).then(r => r.data)
export const getMigrationJob = (id) => api.get(`/migration-jobs/${id}`).then(r => r.data)

// ── Settings: Notification Channels (Phase 6b) ────────────────────────────────

// Workspace-scoped notification channel pool (Phase 3): own + granted globals.
export const fetchWorkspaceNotificationChannels = (ws)       => api.get(`/workspaces/${ws}/notification-channels`).then(r => r.data)
export const createWorkspaceNotificationChannel = (ws, body) => api.post(`/workspaces/${ws}/notification-channels`, body).then(r => r.data)
export const updateWorkspaceNotificationChannel = (ws, id, body) => api.put(`/workspaces/${ws}/notification-channels/${id}`, body).then(r => r.data)
export const deleteWorkspaceNotificationChannel = (ws, id)   => api.delete(`/workspaces/${ws}/notification-channels/${id}`)
export const testWorkspaceNotificationChannel   = (ws, id)   => api.post(`/workspaces/${ws}/notification-channels/${id}/test`).then(r => r.data)

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

// Workspace-scoped alert rules (Phase 3): rules targeting one workspace tier.
export const fetchWorkspaceAlertRules  = (ws)        => api.get(`/workspaces/${ws}/alerts/rules`).then(r => r.data)
export const createWorkspaceAlertRule  = (ws, body)  => api.post(`/workspaces/${ws}/alerts/rules`, body).then(r => r.data)
export const updateWorkspaceAlertRule  = (ws, id, body) => api.put(`/workspaces/${ws}/alerts/rules/${id}`, body).then(r => r.data)
export const deleteWorkspaceAlertRule  = (ws, id)    => api.delete(`/workspaces/${ws}/alerts/rules/${id}`)
export const fetchAlertEvents    = (opts = {}) => api.get('/alerts/events', { params: opts }).then(r => r.data)
export const fetchAlertUnread    = ()         => api.get('/alerts/events/unread-count').then(r => r.data)
export const dismissAlert        = (id)       => api.post(`/alerts/events/${id}/dismiss`).then(r => r.data)
export const dismissAllAlerts    = ()         => api.post('/alerts/events/dismiss-all').then(r => r.data)

// ── WebSocket action helper ───────────────────────────────────────────────────

// `payload` is the full CreateRequest object; it must carry both the parent
// `workspace` (tier name) and the project `name`. The server keys the new
// project at workspaces/{workspace}/projects/{name}.
export function openCreateSocket(payload) {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
  const ws = new WebSocket(`${proto}://${window.location.host}/api/workspaces/create`)
  ws.addEventListener('open', () => {
    const token = useAuthStore.getState().token
    ws.send(JSON.stringify({ token, workspace: payload }))
  })
  return ws
}

export function openActionSocket(workspace, name, command, env, extra = [], services = []) {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
  const ws = new WebSocket(`${proto}://${window.location.host}/api/workspaces/${workspace}/projects/${name}/action`)

  ws.addEventListener('open', () => {
    const token = useAuthStore.getState().token
    ws.send(JSON.stringify({ command, env, extra, services, token }))
  })

  return ws
}

// WebSocket terminal into a container. Nested under workspace → project → env.
export function terminalSocketURL(workspace, name, env) {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
  return `${proto}://${window.location.host}/api/workspaces/${workspace}/projects/${name}/envs/${env}/terminal`
}
