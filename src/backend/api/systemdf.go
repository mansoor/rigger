package api

// Parsing `docker system df`.
//
// Pulled out of the status handler so it can be tested against real output.
// That matters more now that housekeeping can point at any registered host: the
// output is human-formatted, column positions differ per row, and a mixed fleet
// may not run identical Docker versions.
//
// The rows are NOT uniformly shaped — the type name is one token for some rows
// and two for others, which shifts every column after it:
//
//	TYPE            TOTAL     ACTIVE    SIZE      RECLAIMABLE
//	Images          41        27        14.27GB   3.18GB (22%)
//	Containers      37        37        23.72MB   0B (0%)
//	Local Volumes   78        24        14.28GB   5.109GB (35%)
//	Build Cache     182       31        3.086GB   1.758GB
//
// So "Images" puts size at index 3 while "Local Volumes" and "Build Cache" put
// it at 4. Getting that wrong is silent: a count parses as 0 bytes and the row
// simply reports nothing.

import (
	"bufio"
	"strconv"
	"strings"
)

// DiskSection is one row of `docker system df`.
type DiskSection struct {
	Count            int64 `json:"count"`
	SizeBytes        int64 `json:"size_bytes"`
	ReclaimableBytes int64 `json:"reclaimable_bytes"`
}

// DockerDisk is the whole table.
type DockerDisk struct {
	Images     DiskSection `json:"images"`
	Containers DiskSection `json:"containers"`
	Volumes    DiskSection `json:"volumes"`
	BuildCache DiskSection `json:"build_cache"`
}

// Total reports the summed size and reclaimable bytes across every section.
func (d DockerDisk) Total() (size, reclaimable int64) {
	for _, s := range []DiskSection{d.Images, d.Containers, d.Volumes, d.BuildCache} {
		size += s.SizeBytes
		reclaimable += s.ReclaimableBytes
	}
	return size, reclaimable
}

// section reads count/size/reclaimable at the given field offsets, tolerating a
// short row rather than panicking on a format we don't recognise.
func section(f []string, countAt, sizeAt, reclAt int) DiskSection {
	var s DiskSection
	if len(f) > countAt {
		s.Count, _ = strconv.ParseInt(f[countAt], 10, 64)
	}
	if len(f) > sizeAt {
		s.SizeBytes = parseDockerSize(f[sizeAt])
	}
	if len(f) > reclAt {
		// The reclaimable column carries a trailing percentage on some rows
		// ("3.18GB (22%)"); Fields already split it off, so take the number.
		s.ReclaimableBytes = parseDockerSize(f[reclAt])
	}
	return s
}

// parseSystemDF turns `docker system df` output into a DockerDisk.
func parseSystemDF(out string) DockerDisk {
	var d DockerDisk
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		switch f[0] {
		case "Images":
			d.Images = section(f, 1, 3, 4)
		case "Containers":
			d.Containers = section(f, 1, 3, 4)
		case "Local": // "Local Volumes" — the extra word shifts everything by one
			d.Volumes = section(f, 2, 4, 5)
		case "Build": // "Build Cache" — likewise
			d.BuildCache = section(f, 2, 4, 5)
		}
	}
	return d
}
