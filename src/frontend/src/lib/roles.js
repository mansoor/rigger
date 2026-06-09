// Centralized role definitions + plain-language descriptions, shared by the
// invite, add-member, and request-access UIs so every place explains the same
// thing. Mirrors the backend ladder in backend/internal/auth/auth.go.

// Workspace-tier ladder (workspace membership / project overrides), least → most
// privileged. Each level includes everything below it.
export const WS_ROLES = [
  {
    value: 'viewer',
    label: 'Viewer',
    short: 'read-only',
    desc: 'Read-only access. View projects, environments, logs, and metrics. Cannot deploy or change anything.',
  },
  {
    value: 'developer',
    label: 'Developer',
    short: 'operate environments',
    desc: 'Everything a Viewer can do, plus operate environments: deploy, restart, stop/start, edit environment variables, and open container terminals.',
  },
  {
    value: 'operator',
    label: 'Operator',
    short: 'developer + edit project config',
    desc: 'Everything a Developer can do, plus edit project configuration and compose files, and manage pipelines, backups, and rollbacks.',
  },
  {
    value: 'admin',
    label: 'Admin',
    short: 'manage the workspace',
    desc: 'Everything an Operator can do, plus manage members, workspace settings and resources, and create or delete projects.',
  },
]

// Global account roles (Admin → Users). Workspace access is granted separately
// via membership.
export const GLOBAL_ROLES = [
  {
    value: 'user',
    label: 'User',
    short: 'access via workspace membership',
    desc: 'A standard account. Sees only the workspaces they are added to, at the role granted there.',
  },
  {
    value: 'superadmin',
    label: 'Super-admin',
    short: 'full control',
    desc: 'Full control everywhere. Manages all users and global settings, with admin access to every workspace.',
  },
]

// "<Label> — <short>" option lists for <select> dropdowns.
export const wsRoleOptions     = WS_ROLES.map(r => ({ value: r.value, label: `${r.label} — ${r.short}` }))
export const globalRoleOptions = GLOBAL_ROLES.map(r => ({ value: r.value, label: `${r.label} — ${r.short}` }))
