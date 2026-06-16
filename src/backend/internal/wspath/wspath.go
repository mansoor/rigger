// Package wspath centralizes the on-disk layout of the Workspace→Project→Env
// hierarchy so the nested path convention lives in exactly one place.
//
// Layout (root = config.WorkspacesDir):
//
//	{root}/{workspace}/workspace.json          ← marker + workspace metadata
//	{root}/{workspace}/projects/{project}/config.json
//	{root}/{workspace}/projects/{project}/envs/{env}/{.env,docker-compose.yml}
//	{root}/{workspace}/projects/{project}/backups/{env}/{date}/
package wspath

import "path/filepath"

// WorkspaceDir is {root}/{workspace}.
func WorkspaceDir(root, workspace string) string {
	return filepath.Join(root, workspace)
}

// WorkspaceMeta is the workspace.json marker path for a workspace.
func WorkspaceMeta(root, workspace string) string {
	return filepath.Join(root, workspace, "workspace.json")
}

// ProjectsDir is {root}/{workspace}/projects.
func ProjectsDir(root, workspace string) string {
	return filepath.Join(root, workspace, "projects")
}

// ProjectDir is {root}/{workspace}/projects/{project}.
func ProjectDir(root, workspace, project string) string {
	return filepath.Join(root, workspace, "projects", project)
}

// ConfigPath is the config.json path for a project.
func ConfigPath(root, workspace, project string) string {
	return filepath.Join(ProjectDir(root, workspace, project), "config.json")
}

// EnvsDir is the envs/ directory of a project.
func EnvsDir(root, workspace, project string) string {
	return filepath.Join(ProjectDir(root, workspace, project), "envs")
}

// SourceArchive is the canonical path of an upload-source project's stored archive
// (project-level, NOT under any env dir, so it survives env copies). The bytes are
// format-sniffed at extraction, so the fixed name carries no extension.
func SourceArchive(root, workspace, project string) string {
	return filepath.Join(ProjectDir(root, workspace, project), "_source", "archive")
}

// SeedFile is the canonical path of a project's bundled SQL dump chosen for import
// into the managed database (the v3 DB-seed hook). Project-level, beside the source
// archive, so it survives env copies and isn't re-extracted on every build.
func SeedFile(root, workspace, project string) string {
	return filepath.Join(ProjectDir(root, workspace, project), "_source", "seed.sql")
}

// EnvDir is the directory of one environment within a project.
func EnvDir(root, workspace, project, env string) string {
	return filepath.Join(ProjectDir(root, workspace, project), "envs", env)
}

// DotEnv is the .env path for one environment.
func DotEnv(root, workspace, project, env string) string {
	return filepath.Join(EnvDir(root, workspace, project, env), ".env")
}

// BackupsDir is the backups/ directory of a project.
func BackupsDir(root, workspace, project string) string {
	return filepath.Join(ProjectDir(root, workspace, project), "backups")
}

// EnvBackupsDir is the backups directory for one environment.
func EnvBackupsDir(root, workspace, project, env string) string {
	return filepath.Join(BackupsDir(root, workspace, project), env)
}
