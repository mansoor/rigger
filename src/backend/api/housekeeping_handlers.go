package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ── Migration leftovers (Phase 7) ─────────────────────────────────────────────

// GET /api/housekeeping/migration-leftovers — data left on source hosts by
// environment migrations, awaiting cleanup.
func (h *Handler) ListMigrationLeftovers(w http.ResponseWriter, r *http.Request) {
	items, err := h.bridge.ListLeftovers()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// POST /api/housekeeping/migration-leftovers/{id}/clean — permanently wipe the
// source host's containers, volumes and files for this leftover. Streams output.
func (h *Handler) CleanMigrationLeftover(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/clean")
	id, err := parseSettingsID(path, "/api/housekeeping/migration-leftovers/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	fw := &flushWriter{w: w, f: flusher}
	if err := h.bridge.CleanLeftover(id, fw); err != nil {
		fmt.Fprintf(fw, "\n\033[31m✗ cleanup failed: %s\033[0m\n", err.Error())
		return
	}
	fmt.Fprintf(fw, "\n\033[32m✓ done.\033[0m\n")
}

// DELETE /api/housekeeping/migration-leftovers/{id} — drop the record without
// touching the host (already cleaned up manually).
func (h *Handler) DismissMigrationLeftover(w http.ResponseWriter, r *http.Request) {
	id, err := parseSettingsID(r.URL.Path, "/api/housekeeping/migration-leftovers/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.bridge.DismissLeftover(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Docker helpers ─────────────────────────────────────────────────────────────

func dockerRun(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// parseDockerSize converts Docker size strings ("1.5GB", "500MB", "0B") to bytes.
func parseDockerSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "0B" || s == "" {
		return 0
	}
	units := map[string]int64{
		"B": 1, "KB": 1024, "MB": 1024 * 1024, "GB": 1024 * 1024 * 1024,
		"TB": 1024 * 1024 * 1024 * 1024,
	}
	for suffix, mult := range units {
		if strings.HasSuffix(s, suffix) {
			num, err := strconv.ParseFloat(strings.TrimSuffix(s, suffix), 64)
			if err == nil {
				return int64(num * float64(mult))
			}
		}
	}
	return 0
}

// logHousekeeping records a housekeeping action in the DB.
func (h *Handler) logHousekeeping(task, trigger, status, output string, freedBytes, itemsRemoved int64) {
	h.db.Exec(`INSERT INTO housekeeping_log (task, trigger, status, output, freed_bytes, items_removed)
		VALUES (?, ?, ?, ?, ?, ?)`, task, trigger, status, output, freedBytes, itemsRemoved) //nolint:errcheck
}

// ── GET /api/housekeeping/status ──────────────────────────────────────────────

func (h *Handler) HousekeepingStatus(w http.ResponseWriter, r *http.Request) {
	type DiskSection struct {
		Count       int64  `json:"count"`
		SizeBytes   int64  `json:"size_bytes"`
		ReclaimableBytes int64 `json:"reclaimable_bytes"`
	}
	type DockerDisk struct {
		Images     DiskSection `json:"images"`
		Containers DiskSection `json:"containers"`
		Volumes    DiskSection `json:"volumes"`
		BuildCache DiskSection `json:"build_cache"`
	}
	type LastRun struct {
		Task      string `json:"task"`
		RunAt     string `json:"run_at"`
		FreedGB   string `json:"freed_gb"`
	}
	type Result struct {
		Docker      DockerDisk `json:"docker"`
		HealthStatus string    `json:"health_status"` // HEALTHY | CLEANUP_ADVISED | CRITICAL_SPACE_DEFICIT
		LastRuns    []LastRun  `json:"last_runs"`
		HostPrivileged bool    `json:"host_privileged"`
	}

	var res Result

	// Parse docker system df
	out, err := dockerRun("system", "df")
	if err == nil {
		scanner := bufio.NewScanner(strings.NewReader(out))
		for scanner.Scan() {
			line := scanner.Text()
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			switch fields[0] {
			case "Images":
				res.Docker.Images.Count, _ = strconv.ParseInt(fields[1], 10, 64)
				res.Docker.Images.SizeBytes = parseDockerSize(fields[3])
				if len(fields) > 4 {
					recl := strings.Split(fields[4], " ")[0]
					res.Docker.Images.ReclaimableBytes = parseDockerSize(recl)
				}
			case "Containers":
				res.Docker.Containers.Count, _ = strconv.ParseInt(fields[1], 10, 64)
				res.Docker.Containers.SizeBytes = parseDockerSize(fields[3])
				if len(fields) > 4 {
					recl := strings.Split(fields[4], " ")[0]
					res.Docker.Containers.ReclaimableBytes = parseDockerSize(recl)
				}
			case "Local":
				if len(fields) >= 5 {
					res.Docker.Volumes.Count, _ = strconv.ParseInt(fields[2], 10, 64)
					res.Docker.Volumes.SizeBytes = parseDockerSize(fields[4])
					if len(fields) > 5 {
						recl := strings.Split(fields[5], " ")[0]
						res.Docker.Volumes.ReclaimableBytes = parseDockerSize(recl)
					}
				}
			case "Build":
				if len(fields) >= 4 {
					res.Docker.BuildCache.Count, _ = strconv.ParseInt(fields[2], 10, 64)
					res.Docker.BuildCache.SizeBytes = parseDockerSize(fields[3])
					res.Docker.BuildCache.ReclaimableBytes = res.Docker.BuildCache.SizeBytes
				}
			}
		}
	}

	// Health status
	totalReclaimable := res.Docker.Images.ReclaimableBytes +
		res.Docker.Containers.ReclaimableBytes +
		res.Docker.Volumes.ReclaimableBytes +
		res.Docker.BuildCache.ReclaimableBytes

	const GB = int64(1024 * 1024 * 1024)
	switch {
	case totalReclaimable > 10*GB:
		res.HealthStatus = "CRITICAL_SPACE_DEFICIT"
	case totalReclaimable > 2*GB:
		res.HealthStatus = "CLEANUP_ADVISED"
	default:
		res.HealthStatus = "HEALTHY"
	}

	// Check host privileged access
	_, hostErr := exec.Command("nsenter", "--version").Output()
	res.HostPrivileged = hostErr == nil

	// Recent housekeeping log
	rows, _ := h.db.Query(`SELECT task, created_at, freed_bytes FROM housekeeping_log ORDER BY created_at DESC LIMIT 5`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var lr LastRun
			var freed int64
			rows.Scan(&lr.Task, &lr.RunAt, &freed) //nolint:errcheck
			lr.FreedGB = fmt.Sprintf("%.2f GB", float64(freed)/float64(GB))
			res.LastRuns = append(res.LastRuns, lr)
		}
	}
	if res.LastRuns == nil {
		res.LastRuns = []LastRun{}
	}

	writeJSON(w, http.StatusOK, res)
}

// ── GET /api/housekeeping/docker/images ───────────────────────────────────────

func (h *Handler) ListHousekeepingImages(w http.ResponseWriter, r *http.Request) {
	type DockerImage struct {
		ID         string `json:"id"`
		Repository string `json:"repository"`
		Tag        string `json:"tag"`
		Size       string `json:"size"`
		SizeBytes  int64  `json:"size_bytes"`
		Created    string `json:"created"`
		InUse      bool   `json:"in_use"`
	}

	// Get all images (short IDs for display)
	imgOut, err := dockerRun("images", "--format",
		`{"id":"{{.ID}}","repository":"{{.Repository}}","tag":"{{.Tag}}","size":"{{.Size}}","created":"{{.CreatedAt}}"}`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Build a set of in-use image identifiers using two complementary methods:
	//
	// Method 1 — by ImageID (sha256 digest): docker ps returns the full digest
	//   of the image layer, e.g. sha256:abc123...  We normalise to both the
	//   full hex string and the first-12-char short form.
	//
	// Method 2 — by image reference (repository:tag): some Docker versions /
	//   edge cases return an image name here instead of a digest. Storing
	//   "nginx:latest" etc catches those cases too.
	usedSet := map[string]bool{}

	addID := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		usedSet[raw] = true
		stripped := strings.TrimPrefix(raw, "sha256:")
		usedSet[stripped] = true
		if len(stripped) > 12 {
			usedSet[stripped[:12]] = true
		}
	}

	// Method 1: image IDs via {{.ImageID}}
	if idOut, err2 := dockerRun("ps", "-a", "--format", "{{.ImageID}}"); err2 == nil {
		for _, line := range strings.Split(idOut, "\n") {
			addID(line)
		}
	}

	// Method 2: image references via {{.Image}} (e.g. "nginx:latest")
	if refOut, err2 := dockerRun("ps", "-a", "--format", "{{.Image}}"); err2 == nil {
		for _, ref := range strings.Split(refOut, "\n") {
			ref = strings.TrimSpace(ref)
			if ref != "" {
				usedSet[ref] = true
				// also store without tag in case tag differs
				if idx := strings.LastIndex(ref, ":"); idx > 0 {
					usedSet[ref[:idx]] = true
				}
			}
		}
	}

	var images []DockerImage
	for _, line := range strings.Split(imgOut, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		var img DockerImage
		if err := json.Unmarshal([]byte(line), &img); err != nil {
			continue
		}
		img.SizeBytes = parseDockerSize(img.Size)

		// Normalise the image's own ID for lookup
		shortID := strings.TrimPrefix(img.ID, "sha256:")
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}

		// Check in-use by ID (short and full) and by repository:tag reference
		ref := img.Repository + ":" + img.Tag
		img.InUse = usedSet[shortID] ||
			usedSet[img.ID] ||
			usedSet[ref] ||
			usedSet[img.Repository]

		images = append(images, img)
	}
	if images == nil {
		images = []DockerImage{}
	}
	writeJSON(w, http.StatusOK, images)
}

// ── POST /api/housekeeping/docker/prune/dangling-images ───────────────────────

func (h *Handler) PruneDanglingImages(w http.ResponseWriter, r *http.Request) {
	out, err := dockerRun("image", "prune", "-f")
	status := "ok"
	if err != nil {
		status = "error"
	}
	freed := extractFreedBytes(out)
	h.logHousekeeping("prune-dangling-images", "manual", status, out, freed, 0)
	writeJSON(w, http.StatusOK, map[string]any{"output": out, "freed_bytes": freed, "status": status})
}

// ── POST /api/housekeeping/docker/prune/unused-images ────────────────────────

func (h *Handler) PruneUnusedImages(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ImageIDs []string `json:"image_ids"` // empty = prune all unused
	}
	_ = readJSON(r, &body)

	var out string
	var err error
	if len(body.ImageIDs) > 0 {
		// Remove specific images
		args := append([]string{"rmi", "-f"}, body.ImageIDs...)
		out, err = dockerRun(args...)
	} else {
		out, err = dockerRun("image", "prune", "-a", "--filter", "until=168h", "-f")
	}

	status := "ok"
	if err != nil {
		status = "error"
	}
	freed := extractFreedBytes(out)
	h.logHousekeeping("prune-unused-images", "manual", status, out, freed, int64(len(body.ImageIDs)))
	writeJSON(w, http.StatusOK, map[string]any{"output": out, "freed_bytes": freed, "status": status})
}

// ── GET /api/housekeeping/docker/containers ───────────────────────────────────

func (h *Handler) ListStoppedContainers(w http.ResponseWriter, r *http.Request) {
	type StoppedContainer struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Image      string `json:"image"`
		Status     string `json:"status"`
		ExitCode   string `json:"exit_code"`
		FinishedAt string `json:"finished_at"`
		Labels     string `json:"labels"`
	}

	// Build a set of compose project names that currently have running containers.
	// Stopped containers belonging to these projects should not appear here —
	// they are managed by Rigger and may be restarting or intentionally stopped.
	managedProjects := map[string]bool{}
	labelsOut, _ := dockerRun("ps", "--format", "{{.Labels}}")
	for _, labelLine := range strings.Split(labelsOut, "\n") {
		for _, kv := range strings.Split(labelLine, ",") {
			kv = strings.TrimSpace(kv)
			if strings.HasPrefix(kv, "com.docker.compose.project=") {
				proj := strings.TrimPrefix(kv, "com.docker.compose.project=")
				if proj != "" {
					managedProjects[proj] = true
				}
			}
		}
	}

	out, err := dockerRun("ps", "-a", "-f", "status=exited", "-f", "status=dead", "--format",
		`{"id":"{{.ID}}","name":"{{.Names}}","image":"{{.Image}}","status":"{{.Status}}","finished_at":"{{.RunningFor}}","labels":"{{.Labels}}"}`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	var containers []StoppedContainer
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		var c StoppedContainer
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			continue
		}
		// Skip containers that belong to a compose project with running containers —
		// they are managed by Rigger and should not be manually pruned.
		isManaged := false
		for _, kv := range strings.Split(c.Labels, ",") {
			kv = strings.TrimSpace(kv)
			if strings.HasPrefix(kv, "com.docker.compose.project=") {
				proj := strings.TrimPrefix(kv, "com.docker.compose.project=")
				if managedProjects[proj] {
					isManaged = true
					break
				}
			}
		}
		if isManaged {
			continue
		}
		// Extract exit code from status string "Exited (1) 2 hours ago"
		if strings.Contains(c.Status, "Exited (") {
			start := strings.Index(c.Status, "(")
			end := strings.Index(c.Status, ")")
			if start >= 0 && end > start {
				c.ExitCode = c.Status[start+1 : end]
			}
		}
		containers = append(containers, c)
	}
	if containers == nil {
		containers = []StoppedContainer{}
	}
	writeJSON(w, http.StatusOK, containers)
}

// ── POST /api/housekeeping/docker/prune/containers ────────────────────────────

func (h *Handler) PruneContainers(w http.ResponseWriter, r *http.Request) {
	out, err := dockerRun("container", "prune", "-f")
	status := "ok"
	if err != nil {
		status = "error"
	}
	h.logHousekeeping("prune-containers", "manual", status, out, 0, 0)
	writeJSON(w, http.StatusOK, map[string]any{"output": out, "status": status})
}

// ── GET /api/housekeeping/docker/volumes ──────────────────────────────────────

func (h *Handler) ListDanglingVolumes(w http.ResponseWriter, r *http.Request) {
	type DanglingVolume struct {
		Name       string `json:"name"`
		Driver     string `json:"driver"`
		MountPoint string `json:"mount_point"`
		Labels     string `json:"labels"`
	}

	// dangling=true already excludes volumes attached to any container (running or stopped).
	// We additionally exclude volumes that carry a com.docker.compose.project label —
	// these are named volumes declared in compose files and belong to Rigger workspaces.
	out, err := dockerRun("volume", "ls", "-f", "dangling=true", "--format",
		`{"name":"{{.Name}}","driver":"{{.Driver}}","mount_point":"{{.Mountpoint}}","labels":"{{.Labels}}"}`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	var volumes []DanglingVolume
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		var v DanglingVolume
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			continue
		}
		// Skip volumes that belong to a compose project (managed by Rigger).
		if strings.Contains(v.Labels, "com.docker.compose.project") {
			continue
		}
		volumes = append(volumes, v)
	}
	if volumes == nil {
		volumes = []DanglingVolume{}
	}
	writeJSON(w, http.StatusOK, volumes)
}

// ── POST /api/housekeeping/docker/prune/volumes ───────────────────────────────

func (h *Handler) PruneVolumes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		VolumeNames []string `json:"volume_names"` // specific volumes to remove
	}
	_ = readJSON(r, &body)

	var out string
	var err error
	if len(body.VolumeNames) > 0 {
		args := append([]string{"volume", "rm"}, body.VolumeNames...)
		out, err = dockerRun(args...)
	} else {
		out, err = dockerRun("volume", "prune", "-f")
	}

	status := "ok"
	if err != nil {
		status = "error"
	}
	h.logHousekeeping("prune-volumes", "manual", status, out, 0, int64(len(body.VolumeNames)))
	writeJSON(w, http.StatusOK, map[string]any{"output": out, "status": status})
}

// ── POST /api/housekeeping/docker/prune/networks ─────────────────────────────

func (h *Handler) PruneNetworks(w http.ResponseWriter, r *http.Request) {
	out, err := dockerRun("network", "prune", "-f")
	status := "ok"
	if err != nil {
		status = "error"
	}
	h.logHousekeeping("prune-networks", "manual", status, out, 0, 0)
	writeJSON(w, http.StatusOK, map[string]any{"output": out, "status": status})
}

// ── POST /api/housekeeping/docker/prune/build-cache ──────────────────────────

func (h *Handler) PruneBuildCache(w http.ResponseWriter, r *http.Request) {
	out, err := dockerRun("builder", "prune", "-a", "-f")
	status := "ok"
	if err != nil {
		status = "error"
	}
	freed := extractFreedBytes(out)
	h.logHousekeeping("prune-build-cache", "manual", status, out, freed, 0)
	writeJSON(w, http.StatusOK, map[string]any{"output": out, "freed_bytes": freed, "status": status})
}

// ── Host OS helpers ───────────────────────────────────────────────────────────

// nsenter runs a command on the host via nsenter (requires --pid=host or privileged mode).
func nsenterRun(args ...string) (string, error) {
	allArgs := append([]string{"-t", "1", "-m", "-u", "-i", "-n", "--"}, args...)
	cmd := exec.Command("nsenter", allArgs...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ── POST /api/housekeeping/host/apt/clean ─────────────────────────────────────

func (h *Handler) AptClean(w http.ResponseWriter, r *http.Request) {
	out1, err1 := nsenterRun("apt-get", "autoremove", "-y")
	out2, err2 := nsenterRun("apt-get", "clean")
	combined := out1 + "\n" + out2
	status := "ok"
	if err1 != nil || err2 != nil {
		status = "error"
		if err1 != nil {
			combined = "apt-get not available or host access not configured.\n" +
				"Add 'privileged: true' and 'pid: host' to the rigger service in docker-compose.yml to enable host OS operations.\n\n" + combined
		}
	}
	h.logHousekeeping("apt-clean", "manual", status, combined, 0, 0)
	writeJSON(w, http.StatusOK, map[string]any{"output": combined, "status": status})
}

// ── GET /api/housekeeping/host/journal/stats ──────────────────────────────────

func (h *Handler) JournalStats(w http.ResponseWriter, r *http.Request) {
	out, err := nsenterRun("journalctl", "--disk-usage")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"output": "journalctl not accessible. Add 'privileged: true' and 'pid: host' to docker-compose.yml.",
			"available": false,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"output": out, "available": true})
}

// ── POST /api/housekeeping/host/journal/vacuum ────────────────────────────────

func (h *Handler) JournalVacuum(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MaxAgeDays int `json:"max_age_days"`
		MaxSizeGB  int `json:"max_size_gb"`
	}
	body.MaxAgeDays = 14
	body.MaxSizeGB = 2
	_ = readJSON(r, &body)

	var out string
	var err error
	if body.MaxAgeDays > 0 {
		out, err = nsenterRun("journalctl", fmt.Sprintf("--vacuum-time=%dd", body.MaxAgeDays))
	} else {
		out, err = nsenterRun("journalctl", fmt.Sprintf("--vacuum-size=%dG", body.MaxSizeGB))
	}

	status := "ok"
	if err != nil {
		status = "error"
	}
	h.logHousekeeping("journal-vacuum", "manual", status, out, 0, 0)
	writeJSON(w, http.StatusOK, map[string]any{"output": out, "status": status})
}

// ── GET /api/housekeeping/host/kernels ────────────────────────────────────────

func (h *Handler) ListKernels(w http.ResponseWriter, r *http.Request) {
	type KernelInfo struct {
		Package string `json:"package"`
		Version string `json:"version"`
		Active  bool   `json:"active"`
		Locked  bool   `json:"locked"` // active + previous
	}

	activeOut, _ := nsenterRun("uname", "-r")
	active := strings.TrimSpace(activeOut)

	dpkgOut, err := nsenterRun("dpkg", "-l", "linux-image-*")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"kernels": []KernelInfo{},
			"available": false,
			"active": active,
		})
		return
	}

	var kernels []KernelInfo
	lines := strings.Split(dpkgOut, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "ii ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pkg := fields[1]
		ver := strings.TrimPrefix(pkg, "linux-image-")
		isActive := strings.Contains(ver, active)
		// Lock the active kernel and the one immediately before it
		isPrev := i > 0 && len(kernels) > 0 && len(kernels) == 1
		kernels = append(kernels, KernelInfo{
			Package: pkg, Version: ver,
			Active: isActive, Locked: isActive || isPrev,
		})
	}

	// Ensure the newest non-active kernel is also locked as "previous"
	if len(kernels) >= 2 {
		for i := range kernels {
			if !kernels[i].Active {
				kernels[i].Locked = true
				break
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"kernels": kernels, "active": active, "available": true,
	})
}

// ── POST /api/housekeeping/host/kernels/clean ─────────────────────────────────

func (h *Handler) CleanKernels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Packages []string `json:"packages"`
	}
	if err := readJSON(r, &body); err != nil || len(body.Packages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "packages list required"})
		return
	}
	args := append([]string{"apt-get", "purge", "-y"}, body.Packages...)
	out, err := nsenterRun(args...)
	status := "ok"
	if err != nil {
		status = "error"
	}
	h.logHousekeeping("clean-kernels", "manual", status, out, 0, int64(len(body.Packages)))
	writeJSON(w, http.StatusOK, map[string]any{"output": out, "status": status})
}

// ── POST /api/housekeeping/host/tmp/clean ─────────────────────────────────────

func (h *Handler) CleanTmp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MaxAgeDays int      `json:"max_age_days"`
		Exclude    []string `json:"exclude"` // patterns to exclude
	}
	body.MaxAgeDays = 7
	_ = readJSON(r, &body)

	atimeArg := fmt.Sprintf("+%d", body.MaxAgeDays)
	findArgs := []string{"find", "/tmp", "-type", "f", "-atime", atimeArg}
	for _, pat := range body.Exclude {
		findArgs = append(findArgs, "!", "-name", pat)
	}
	findArgs = append(findArgs, "-delete")
	out, err := nsenterRun(findArgs...)

	status := "ok"
	if err != nil {
		status = "error"
	}
	h.logHousekeeping("clean-tmp", "manual", status, out, 0, 0)
	writeJSON(w, http.StatusOK, map[string]any{"output": out, "status": status})
}

// ── GET /api/housekeeping/log ─────────────────────────────────────────────────

func (h *Handler) HousekeepingLog(w http.ResponseWriter, r *http.Request) {
	type LogEntry struct {
		ID           int64  `json:"id"`
		Task         string `json:"task"`
		Trigger      string `json:"trigger"`
		Status       string `json:"status"`
		Output       string `json:"output"`
		FreedBytes   int64  `json:"freed_bytes"`
		ItemsRemoved int64  `json:"items_removed"`
		CreatedAt    string `json:"created_at"`
	}

	rows, err := h.db.Query(`SELECT id, task, trigger, status, output, freed_bytes, items_removed, created_at
		FROM housekeeping_log ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	var entries []LogEntry
	for rows.Next() {
		var e LogEntry
		rows.Scan(&e.ID, &e.Task, &e.Trigger, &e.Status, &e.Output, &e.FreedBytes, &e.ItemsRemoved, &e.CreatedAt) //nolint:errcheck
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []LogEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// extractFreedBytes parses "Total reclaimed space: 1.5GB" from docker prune output.
func extractFreedBytes(output string) int64 {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Total reclaimed space:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return parseDockerSize(strings.TrimSpace(parts[1]))
			}
		}
	}
	return 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// HousekeepingAutoRun is called on a schedule to run safe/automated tasks.
func (h *Handler) HousekeepingAutoRun() {
	tasks := []struct {
		name string
		args []string
	}{
		{"prune-networks", []string{"network", "prune", "-f"}},
		{"prune-dangling-images", []string{"image", "prune", "-f"}},
	}
	for _, t := range tasks {
		out, err := dockerRun(t.args...)
		status := "ok"
		if err != nil {
			status = "error"
		}
		freed := extractFreedBytes(out)
		h.logHousekeeping(t.name, "cron", status, out, freed, 0)
	}
}

// HousekeepingAutoRunAt schedules the automated tasks daily at the given hour (UTC).
func (h *Handler) StartHousekeepingScheduler(hourUTC int) {
	go func() {
		for {
			now := time.Now().UTC()
			next := time.Date(now.Year(), now.Month(), now.Day(), hourUTC, 0, 0, 0, time.UTC)
			if now.After(next) {
				next = next.Add(24 * time.Hour)
			}
			time.Sleep(time.Until(next))
			h.HousekeepingAutoRun()
		}
	}()
}

// formatBytes converts bytes to human-readable string — exported for template use.
func formatBytesHK(b int64) string {
	const GB = 1024 * 1024 * 1024
	const MB = 1024 * 1024
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// ensure formatBytesHK is used (suppress unused warning)
var _ = formatBytesHK

