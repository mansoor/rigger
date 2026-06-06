package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mansoor/rigger/ui/api"
	"github.com/mansoor/rigger/ui/internal/alerts"
	"github.com/mansoor/rigger/ui/internal/auth"
	"github.com/mansoor/rigger/ui/internal/config"
	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/db"
	"github.com/mansoor/rigger/ui/internal/imagecheck"
	"github.com/mansoor/rigger/ui/internal/metrics"
	"github.com/mansoor/rigger/ui/internal/notify"
	"github.com/mansoor/rigger/ui/internal/remotehost"
	"github.com/mansoor/rigger/ui/internal/shell"
)

//go:embed all:dist
var frontendFS embed.FS

func main() {
	// Subcommands run a one-shot task and exit instead of starting the server.
	if len(os.Args) > 1 && os.Args[1] == "init-workspace" {
		os.Exit(runInitWorkspace(os.Args[2:]))
	}

	cfg := config.Load()

	// ── Database ──────────────────────────────────────────────────────────────
	database, err := db.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("db: %v", err)
	}

	// ── Services ──────────────────────────────────────────────────────────────
	authSvc := auth.NewService(database, cfg.JWTSecret, cfg.JWTExpiry)
	// Phase 7: SSH connection pool + the AES key (derived from JWT secret) that
	// decrypts host keys, so the bridge can run commands on remote workspaces.
	hostPool := remotehost.NewPool()
	cryptoKey, _ := crypto.DeriveKey([]byte(cfg.JWTSecret))
	bridge := shell.NewBridge(cfg.WorkspacesDir, cfg.RemoteWorkspacesDir, cfg.ToolkitRoot, database, hostPool, cryptoKey)

	// Phase 6.5 finish: commands run natively in Go, so workspaces no longer
	// need a generated run.sh. Sweep away any leftover from older versions.
	removeRunSh(cfg.WorkspacesDir)

	// Image update cache — populated by hourly background checker
	imgCache := imagecheck.NewCache()
	imagecheck.RunBackground(imgCache, cfg.WorkspacesDir)

	// Notifications (Phase 6b): email is delivered directly over SMTP; everything
	// else via Apprise. The Apprise backend is in-process apprise-go by default
	// (no sidecar); setting APPRISE_URL switches to the Apprise API sidecar.
	var appriseBackend notify.AppriseBackend
	if cfg.AppriseURL != "" {
		appriseBackend = notify.NewAppriseClient(cfg.AppriseURL)
		log.Printf("Notifications: Apprise sidecar at %s", cfg.AppriseURL)
	} else {
		appriseBackend = notify.NewEmbeddedApprise()
		log.Printf("Notifications: embedded apprise-go")
	}
	notifier := notify.NewDispatcher(database, appriseBackend)

	// Alerting (Phase 6): SSE broker for pushing alert events + the background
	// rule evaluator that turns rule conditions into alert events every 60s and
	// dispatches notifications to each rule's assigned channels.
	alertBroker := alerts.NewBroker()
	alerts.NewEvaluator(database, cfg.WorkspacesDir, imgCache, alertBroker, notifier).Run()

	// Metrics history (Phase 6d): background collector samples per-env CPU/memory/
	// disk every METRICS_INTERVAL_SECONDS (default 30s). A separate tiered job
	// thins old rows (full-res ≤24h, 1-min 24–120h, 5-min beyond) and prunes to
	// 90 days. The same interval is reported to the UI via /api/metrics/config.
	metricsInterval := time.Duration(metrics.IntervalSeconds()) * time.Second
	metrics.NewCollector(database, cfg.WorkspacesDir, metricsInterval, bridge).Run()

	handler := api.NewHandler(authSvc, database, bridge, cfg.WorkspacesDir, cfg.RemoteWorkspacesDir, cfg.TemplatesDir, cfg.DataDir, imgCache, alertBroker, notifier, cfg.JWTSecret)

	// Start daily automated housekeeping (networks + dangling images) at 03:00 UTC
	handler.StartHousekeepingScheduler(3)

	// ── Router ────────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	// Setup / auth (no JWT required)
	mux.HandleFunc("GET /api/setup/status", handler.SetupStatus)
	mux.HandleFunc("POST /api/setup", handler.Setup)
	mux.HandleFunc("POST /api/auth/login", handler.Login)
	mux.HandleFunc("POST /api/auth/logout", handler.Logout)
	mux.HandleFunc("POST /api/auth/refresh", handler.Refresh)
	mux.Handle("POST /api/auth/password", authSvc.Middleware(http.HandlerFunc(handler.ChangePassword)))

	// Protected API routes (JWT middleware applied per-route group)
	protected := authSvc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/templates":
			handler.ListTemplates(w, r)
		case r.Method == "POST" && r.URL.Path == "/api/tools/save-template":
			handler.SaveTemplate(w, r)
		case r.Method == "POST" && r.URL.Path == "/api/tools/workspace-backup":
			handler.StartWorkspaceBackup(w, r)
		case r.Method == "GET" && matchPrefix(r.URL.Path, "/api/tools/backup-jobs/"):
			r.SetPathValue("id", pathSegment(r.URL.Path, 3))
			handler.GetBackupJob(w, r)
		case r.Method == "GET" && r.URL.Path == "/api/tools/workspace-archives":
			handler.ListWorkspaceArchives(w, r)
		case r.Method == "GET" && matchPrefix(r.URL.Path, "/api/tools/workspace-archives/"):
			r.SetPathValue("filename", pathSegment(r.URL.Path, 3))
			handler.DownloadWorkspaceArchive(w, r)
		case r.Method == "DELETE" && matchPrefix(r.URL.Path, "/api/tools/workspace-archives/"):
			r.SetPathValue("filename", pathSegment(r.URL.Path, 3))
			handler.DeleteWorkspaceArchive(w, r)
		case r.Method == "POST" && r.URL.Path == "/api/tools/workspace-restore":
			handler.RestoreWorkspace(w, r)
		// Configuration snapshots (.rws) — config-only, separate from full backups.
		case r.Method == "POST" && r.URL.Path == "/api/tools/workspace-snapshots":
			handler.CreateWorkspaceSnapshot(w, r)
		case r.Method == "GET" && r.URL.Path == "/api/tools/workspace-snapshots":
			handler.ListWorkspaceSnapshots(w, r)
		case r.Method == "POST" && matchPrefix(r.URL.Path, "/api/tools/workspace-snapshots/") && hasSuffix(r.URL.Path, "/rollback"):
			r.SetPathValue("filename", pathSegment(r.URL.Path, 3))
			handler.RollbackWorkspaceSnapshot(w, r)
		case r.Method == "GET" && matchPrefix(r.URL.Path, "/api/tools/workspace-snapshots/"):
			r.SetPathValue("filename", pathSegment(r.URL.Path, 3))
			handler.DownloadWorkspaceSnapshot(w, r)
		case r.Method == "DELETE" && matchPrefix(r.URL.Path, "/api/tools/workspace-snapshots/"):
			r.SetPathValue("filename", pathSegment(r.URL.Path, 3))
			handler.DeleteWorkspaceSnapshot(w, r)
		case r.Method == "POST" && matchPrefix(r.URL.Path, "/api/templates/") && hasSuffix(r.URL.Path, "/use"):
			r.SetPathValue("name", pathSegment(r.URL.Path, 2))
			handler.RecordTemplateUse(w, r)
		case r.Method == "GET" && matchPrefix(r.URL.Path, "/api/templates/"):
			r.SetPathValue("name", pathSegment(r.URL.Path, 2))
			handler.GetTemplate(w, r)
		case r.Method == "GET" && r.URL.Path == "/api/debug/paths":
			handler.DebugPaths(w, r)
		case r.Method == "GET" && r.URL.Path == "/api/metrics/config":
			handler.GetMetricsConfig(w, r)
		case r.Method == "GET" && r.URL.Path == "/api/stats":
			handler.GetStats(w, r)
		case r.Method == "GET" && r.URL.Path == "/api/live-stats":
			handler.GetLiveStats(w, r)
		case r.Method == "GET" && r.URL.Path == "/api/activity":
			handler.GetAllActivity(w, r)
		case r.Method == "POST" && r.URL.Path == "/api/port-check":
			handler.PortCheck(w, r)
		case r.Method == "GET" && r.URL.Path == "/api/backups":
			handler.ListBackups(w, r)
		case r.Method == "DELETE" && matchPrefix(r.URL.Path, "/api/backups/"):
			// /api/backups/{workspace}/{env}/{date}
			r.SetPathValue("workspace", pathSegment(r.URL.Path, 2))
			r.SetPathValue("env", pathSegment(r.URL.Path, 3))
			r.SetPathValue("date", pathSegment(r.URL.Path, 4))
			handler.DeleteBackup(w, r)
		case r.Method == "GET" && r.URL.Path == "/api/workspaces":
			handler.ListWorkspaces(w, r)
		case r.Method == "GET" && matchPrefix(r.URL.Path, "/api/workspaces/") && !hasSuffix(r.URL.Path, "/action"):
			// parts[2]=name, parts[3]=sub (activity|envs), parts[4]=env, parts[5]=subsub (status|vars)
			name := pathSegment(r.URL.Path, 2)
			sub := pathSegment(r.URL.Path, 3)  // activity | envs | config
			env := pathSegment(r.URL.Path, 4)  // env name (when sub=envs)
			subsub := pathSegment(r.URL.Path, 5) // vars | status | compose
			r.SetPathValue("name", name)
			r.SetPathValue("env", env)
			switch {
			case sub == "activity":
				handler.GetActivity(w, r)
			case sub == "action-runs":
				handler.GetActionRuns(w, r)
			case sub == "config":
				handler.GetConfig(w, r)
			case sub == "envs" && subsub == "status":
				handler.GetEnvStatus(w, r)
			case sub == "envs" && subsub == "vars":
				handler.GetEnvVars(w, r)
			case sub == "envs" && subsub == "compose":
				handler.GetCompose(w, r)
			case sub == "envs" && subsub == "image-updates":
				handler.GetImageUpdates(w, r)
			case sub == "envs" && subsub == "containers":
				// .../containers            → list; .../containers/{service}/{action} → per-container
				if svc := pathSegment(r.URL.Path, 6); svc != "" {
					r.SetPathValue("service", svc)
					switch pathSegment(r.URL.Path, 7) {
					case "inspect":
						handler.ContainerInspect(w, r)
					case "stats":
						handler.ContainerStats(w, r)
					case "top":
						handler.ContainerTop(w, r)
					case "image-history":
						handler.ContainerImageHistory(w, r)
					case "files":
						handler.FileList(w, r)
					case "file":
						handler.FileView(w, r)
					case "file-download":
						handler.FileDownload(w, r)
					default:
						http.NotFound(w, r)
					}
				} else {
					handler.GetContainers(w, r)
				}
			case sub == "envs" && subsub == "metrics":
				handler.GetEnvMetrics(w, r)
			default:
				handler.GetWorkspace(w, r)
			}
		case r.Method == "POST" && matchPrefix(r.URL.Path, "/api/workspaces/") && hasSuffix(r.URL.Path, "/action"):
			// REST streaming action for the `rigger` CLI (6.5d):
			// /api/workspaces/{name}/envs/{env}/action
			r.SetPathValue("name", pathSegment(r.URL.Path, 2))
			r.SetPathValue("env", pathSegment(r.URL.Path, 4))
			handler.ActionHTTP(w, r)
		case r.Method == "POST" && matchPrefix(r.URL.Path, "/api/workspaces/") && pathSegment(r.URL.Path, 5) == "containers":
			// /api/workspaces/{name}/envs/{env}/containers/{service}/{action}
			r.SetPathValue("name", pathSegment(r.URL.Path, 2))
			r.SetPathValue("env", pathSegment(r.URL.Path, 4))
			r.SetPathValue("service", pathSegment(r.URL.Path, 6))
			switch pathSegment(r.URL.Path, 7) {
			case "files-upload":
				handler.FileUpload(w, r)
			case "file-rename":
				handler.FileRename(w, r)
			case "file-chmod":
				handler.FileChmod(w, r)
			case "file-mkdir":
				handler.FileMkdir(w, r)
			case "file-new":
				handler.FileNew(w, r)
			default:
				http.NotFound(w, r)
			}
		case r.Method == "POST" && matchPrefix(r.URL.Path, "/api/workspaces/") && hasSuffix(r.URL.Path, "/export-template"):
			r.SetPathValue("name", pathSegment(r.URL.Path, 2))
			handler.ExportTemplate(w, r)
		case r.Method == "POST" && matchPrefix(r.URL.Path, "/api/workspaces/") && hasSuffix(r.URL.Path, "/migrate"):
			// /api/workspaces/{name}/migrate — move a workspace to another host (Phase 7)
			r.SetPathValue("name", pathSegment(r.URL.Path, 2))
			handler.MigrateWorkspace(w, r)
		case r.Method == "PUT" && matchPrefix(r.URL.Path, "/api/workspaces/"):
			name := pathSegment(r.URL.Path, 2)
			sub := pathSegment(r.URL.Path, 3)
			env := pathSegment(r.URL.Path, 4)
			subsub := pathSegment(r.URL.Path, 5)
			r.SetPathValue("name", name)
			r.SetPathValue("env", env)
			switch {
			case sub == "config":
				handler.PutConfig(w, r)
			case sub == "envs" && subsub == "compose":
				handler.PutCompose(w, r)
			case sub == "envs" && subsub == "host":
				handler.SetEnvHost(w, r)
			case sub == "envs" && subsub == "containers" && pathSegment(r.URL.Path, 7) == "file":
				r.SetPathValue("service", pathSegment(r.URL.Path, 6))
				handler.FileSave(w, r)
			default:
				http.NotFound(w, r)
			}
		case r.Method == "DELETE" && matchPrefix(r.URL.Path, "/api/workspaces/"):
			if pathSegment(r.URL.Path, 5) == "containers" && pathSegment(r.URL.Path, 7) == "file" {
				// /api/workspaces/{name}/envs/{env}/containers/{service}/file?path=…
				r.SetPathValue("name", pathSegment(r.URL.Path, 2))
				r.SetPathValue("env", pathSegment(r.URL.Path, 4))
				r.SetPathValue("service", pathSegment(r.URL.Path, 6))
				handler.FileDelete(w, r)
			} else if pathSegment(r.URL.Path, 3) == "action-runs" {
				r.SetPathValue("name", pathSegment(r.URL.Path, 2))
				handler.ClearActionRuns(w, r)
			} else {
				r.SetPathValue("name", pathSegment(r.URL.Path, 2))
				handler.DeleteWorkspace(w, r)
			}
		case r.Method == "PATCH" && matchPrefix(r.URL.Path, "/api/workspaces/"):
			name := pathSegment(r.URL.Path, 2)
			env := pathSegment(r.URL.Path, 4)
			r.SetPathValue("name", name)
			r.SetPathValue("env", env)
			handler.UpdateEnvVars(w, r)
		default:
			http.NotFound(w, r)
		}
	}))

	mux.Handle("/api/debug/", protected)
	mux.Handle("/api/workspaces", protected)
	mux.Handle("/api/workspaces/", protected)

	// SSE: Docker container events (auth via ?token= query param)
	mux.HandleFunc("/api/events", handler.StreamEvents)

	// WebSocket: create workspace (streams bootstrap output)
	mux.HandleFunc("/api/workspaces/create", handler.CreateWorkspace)

	// Stats (dashboard)
	mux.Handle("/api/metrics/config", protected)
	mux.Handle("/api/stats", protected)
	mux.Handle("/api/live-stats", protected)

	// Global activity feed
	mux.Handle("/api/activity", protected)

	// Host-aware port-conflict check (New/Edit workspace)
	mux.Handle("/api/port-check", protected)

	// Backups (cross-workspace listing)
	mux.Handle("/api/backups", protected)
	mux.Handle("/api/backups/", protected)

	// Templates
	mux.Handle("/api/templates", protected)
	mux.Handle("/api/templates/", protected)
	mux.Handle("/api/tools/", protected)

	// Settings (backup targets + docker registries) — all protected
	mux.Handle("/api/settings/", authSvc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		// General settings (ACME email, Rigger domain)
		case r.Method == "GET" && path == "/api/settings/general":
			handler.GetGeneralSettings(w, r)
		case r.Method == "PUT" && path == "/api/settings/general":
			handler.PutGeneralSettings(w, r)
		// Backup targets
		case r.Method == "GET" && path == "/api/settings/backup-targets":
			handler.ListBackupTargets(w, r)
		case r.Method == "POST" && path == "/api/settings/backup-targets":
			handler.CreateBackupTarget(w, r)
		case r.Method == "PUT" && matchPrefix(path, "/api/settings/backup-targets/"):
			handler.UpdateBackupTarget(w, r)
		case r.Method == "DELETE" && matchPrefix(path, "/api/settings/backup-targets/"):
			handler.DeleteBackupTarget(w, r)
		// Docker registries
		case r.Method == "GET" && path == "/api/settings/registries":
			handler.ListRegistries(w, r)
		case r.Method == "POST" && path == "/api/settings/registries":
			handler.CreateRegistry(w, r)
		case r.Method == "PUT" && matchPrefix(path, "/api/settings/registries/") && !hasSuffix(path, "/test"):
			handler.UpdateRegistry(w, r)
		case r.Method == "DELETE" && matchPrefix(path, "/api/settings/registries/"):
			handler.DeleteRegistry(w, r)
		case r.Method == "POST" && matchPrefix(path, "/api/settings/registries/") && hasSuffix(path, "/test"):
			handler.TestRegistry(w, r)
		// Notification channels (6b)
		case r.Method == "GET" && path == "/api/settings/notification-channels":
			handler.ListNotificationChannels(w, r)
		case r.Method == "POST" && path == "/api/settings/notification-channels":
			handler.CreateNotificationChannel(w, r)
		case r.Method == "POST" && matchPrefix(path, "/api/settings/notification-channels/") && hasSuffix(path, "/test"):
			handler.TestNotificationChannel(w, r)
		case r.Method == "PUT" && matchPrefix(path, "/api/settings/notification-channels/"):
			handler.UpdateNotificationChannel(w, r)
		case r.Method == "DELETE" && matchPrefix(path, "/api/settings/notification-channels/"):
			handler.DeleteNotificationChannel(w, r)
		default:
			http.NotFound(w, r)
		}
	})))

	// Hosts (Phase 7: Multi-Host Support) — CRUD + SSH connectivity test
	mux.Handle("/api/hosts", authSvc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			handler.ListHosts(w, r)
		case "POST":
			handler.CreateHost(w, r)
		default:
			http.NotFound(w, r)
		}
	})))
	mux.Handle("/api/hosts/", authSvc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == "GET" && path == "/api/hosts/managed-key":
			handler.ManagedHostKey(w, r)
		case r.Method == "GET" && hasSuffix(path, "/stats"):
			handler.HostStats(w, r)
		case r.Method == "POST" && hasSuffix(path, "/test"):
			handler.TestHost(w, r)
		case r.Method == "POST" && hasSuffix(path, "/scan"):
			handler.ScanHost(w, r)
		case r.Method == "POST" && hasSuffix(path, "/import"):
			handler.ImportHost(w, r)
		case r.Method == "PUT":
			handler.UpdateHost(w, r)
		case r.Method == "DELETE":
			handler.DeleteHost(w, r)
		default:
			http.NotFound(w, r)
		}
	})))

	// Migration jobs (Phase 7) — poll async workspace/env host moves
	mux.Handle("/api/migration-jobs/", authSvc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			r.SetPathValue("id", pathSegment(r.URL.Path, 2))
			handler.GetMigrationJob(w, r)
			return
		}
		http.NotFound(w, r)
	})))

	// Alerts (Phase 6) — rules CRUD + events inbox, all JWT-protected
	mux.Handle("/api/alerts/", authSvc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		// Rules (6a)
		case r.Method == "GET" && path == "/api/alerts/rules":
			handler.ListAlertRules(w, r)
		case r.Method == "POST" && path == "/api/alerts/rules":
			handler.CreateAlertRule(w, r)
		case r.Method == "PUT" && matchPrefix(path, "/api/alerts/rules/"):
			handler.UpdateAlertRule(w, r)
		case r.Method == "DELETE" && matchPrefix(path, "/api/alerts/rules/"):
			handler.DeleteAlertRule(w, r)
		// Events / inbox (6c) — exact paths before the generic dismiss matcher
		case r.Method == "GET" && path == "/api/alerts/summary":
			handler.AlertSummary(w, r)
		case r.Method == "GET" && path == "/api/alerts/events/unread-count":
			handler.AlertUnreadCount(w, r)
		case r.Method == "GET" && path == "/api/alerts/events":
			handler.ListAlertEvents(w, r)
		case r.Method == "POST" && path == "/api/alerts/events/dismiss-all":
			handler.DismissAllAlerts(w, r)
		case r.Method == "POST" && matchPrefix(path, "/api/alerts/events/") && hasSuffix(path, "/dismiss"):
			handler.DismissAlert(w, r)
		case r.Method == "GET" && path == "/api/alerts/meta":
			handler.AlertMeta(w, r)
		default:
			http.NotFound(w, r)
		}
	})))

	// Housekeeping — all JWT-protected
	mux.Handle("/api/housekeeping/", authSvc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == "GET"  && path == "/api/housekeeping/status":
			handler.HousekeepingStatus(w, r)
		case r.Method == "GET"  && path == "/api/housekeeping/log":
			handler.HousekeepingLog(w, r)
		// Migration leftovers (Phase 7)
		case r.Method == "GET" && path == "/api/housekeeping/migration-leftovers":
			handler.ListMigrationLeftovers(w, r)
		case r.Method == "POST" && matchPrefix(path, "/api/housekeeping/migration-leftovers/") && hasSuffix(path, "/clean"):
			handler.CleanMigrationLeftover(w, r)
		case r.Method == "DELETE" && matchPrefix(path, "/api/housekeeping/migration-leftovers/"):
			handler.DismissMigrationLeftover(w, r)
		// Docker images
		case r.Method == "GET"  && path == "/api/housekeeping/docker/images":
			handler.ListHousekeepingImages(w, r)
		case r.Method == "POST" && path == "/api/housekeeping/docker/prune/dangling-images":
			handler.PruneDanglingImages(w, r)
		case r.Method == "POST" && path == "/api/housekeeping/docker/prune/unused-images":
			handler.PruneUnusedImages(w, r)
		// Docker containers
		case r.Method == "GET"  && path == "/api/housekeeping/docker/containers":
			handler.ListStoppedContainers(w, r)
		case r.Method == "POST" && path == "/api/housekeeping/docker/prune/containers":
			handler.PruneContainers(w, r)
		// Docker volumes
		case r.Method == "GET"  && path == "/api/housekeeping/docker/volumes":
			handler.ListDanglingVolumes(w, r)
		case r.Method == "POST" && path == "/api/housekeeping/docker/prune/volumes":
			handler.PruneVolumes(w, r)
		// Docker networks & build cache
		case r.Method == "POST" && path == "/api/housekeeping/docker/prune/networks":
			handler.PruneNetworks(w, r)
		case r.Method == "POST" && path == "/api/housekeeping/docker/prune/build-cache":
			handler.PruneBuildCache(w, r)
		// Host OS
		case r.Method == "POST" && path == "/api/housekeeping/host/apt/clean":
			handler.AptClean(w, r)
		case r.Method == "GET"  && path == "/api/housekeeping/host/journal/stats":
			handler.JournalStats(w, r)
		case r.Method == "POST" && path == "/api/housekeeping/host/journal/vacuum":
			handler.JournalVacuum(w, r)
		case r.Method == "GET"  && path == "/api/housekeeping/host/kernels":
			handler.ListKernels(w, r)
		case r.Method == "POST" && path == "/api/housekeeping/host/kernels/clean":
			handler.CleanKernels(w, r)
		case r.Method == "POST" && path == "/api/housekeeping/host/tmp/clean":
			handler.CleanTmp(w, r)
		default:
			http.NotFound(w, r)
		}
	})))

	// WebSocket action endpoint — auth via token in first WS message
	mux.HandleFunc("/api/workspaces/{name}/action", func(w http.ResponseWriter, r *http.Request) {
		handler.RunAction(w, r)
	})

	// WebSocket terminal — interactive shell into a container
	mux.HandleFunc("/api/workspaces/{name}/envs/{env}/terminal", func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("name", r.PathValue("name"))
		r.SetPathValue("env", pathSegment(r.URL.Path, 4))
		handler.Terminal(w, r)
	})

	// ── Static frontend (SPA) ────────────────────────────────────────────────
	distFS, err := fs.Sub(frontendFS, "dist")
	if err != nil {
		log.Fatalf("embed fs: %v", err)
	}
	spa := spaHandler{fs: http.FS(distFS)}
	mux.Handle("/", spa)

	// ── Start server ──────────────────────────────────────────────────────────
	log.Printf("Rigger UI listening on %s", cfg.ListenAddr)
	log.Printf("Toolkit root : %s", cfg.ToolkitRoot)
	log.Printf("Workspaces   : %s", cfg.WorkspacesDir)

	if err := http.ListenAndServe(cfg.ListenAddr, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// spaHandler serves the React SPA: known assets are served directly;
// everything else falls back to index.html for client-side routing.
type spaHandler struct{ fs http.FileSystem }

func (s spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f, err := s.fs.Open(r.URL.Path)
	if err != nil {
		// Serve index.html for any unknown path (client-side routing)
		r.URL.Path = "/"
		http.FileServer(s.fs).ServeHTTP(w, r)
		return
	}
	f.Close()
	http.FileServer(s.fs).ServeHTTP(w, r)
}

// ── URL helpers ───────────────────────────────────────────────────────────────

func matchPrefix(path, prefix string) bool {
	return len(path) >= len(prefix) && path[:len(prefix)] == prefix
}

func hasSuffix(path, suffix string) bool {
	return len(path) >= len(suffix) && path[len(path)-len(suffix):] == suffix
}

func pathSegment(path string, n int) string {
	parts := splitPath(path)
	if n < len(parts) {
		return parts[n]
	}
	return ""
}

// removeRunSh deletes the generated run.sh from every workspace. Commands now run
// natively in Go (Phase 6.5 finish); run.sh is a stale generated artifact, so
// removing it is safe (no user data). Idempotent — absent files are ignored.
func removeRunSh(workspacesDir string) {
	entries, err := os.ReadDir(workspacesDir)
	if err != nil {
		return
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		runSh := filepath.Join(workspacesDir, e.Name(), "run.sh")
		if err := os.Remove(runSh); err == nil {
			removed++
		}
	}
	if removed > 0 {
		log.Printf("removeRunSh: removed stale run.sh from %d workspace(s)", removed)
	}
}

func splitPath(path string) []string {
	var parts []string
	cur := ""
	for _, c := range path {
		if c == '/' {
			if cur != "" {
				parts = append(parts, cur)
				cur = ""
			}
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		parts = append(parts, cur)
	}
	return parts
}
