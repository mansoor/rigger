package api

// Listing and purging old kernel packages.
//
// Split out of the handlers for two reasons. The parsing deserves tests — it
// decides which kernel is safe to delete, and getting that wrong on a remote
// production host is not a cosmetic bug. And the purge path needs the SAME
// answer the list produced, so both call one function.
//
// Debian-family only. Kernel retention on RHEL-family is a different mechanism
// (dnf's installonly_limit prunes automatically) and purging kernels there by
// hand is a good way to break a machine, so we report it unsupported rather than
// approximating it.

import (
	"strconv"
	"strings"
)

type kernelInfo struct {
	Package string `json:"package"`
	Version string `json:"version"`
	Active  bool   `json:"active"`
	Locked  bool   `json:"locked"` // never offered for removal
}

// parseKernels reads `dpkg -l linux-image-*` and marks what must not be removed:
// the running kernel, and the next-best one to fall back to if the running
// kernel fails to boot. Leaving a machine with exactly one bootable kernel is
// how a bad update becomes an unbootable server.
func parseKernels(dpkgOut, active string) []kernelInfo {
	active = strings.TrimSpace(active)

	var kernels []kernelInfo
	for _, line := range strings.Split(dpkgOut, "\n") {
		// "ii" = installed. Other states (rc = removed, config left) hold no
		// disk worth reclaiming and no bootable image.
		if !strings.HasPrefix(line, "ii ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pkg := fields[1]
		// Meta-packages ("linux-image-generic") track whichever kernel is
		// current; they carry no image of their own and purging one drags the
		// real kernel out with it.
		ver := strings.TrimPrefix(pkg, "linux-image-")
		if !hasVersionDigits(ver) {
			continue
		}
		kernels = append(kernels, kernelInfo{
			Package: pkg, Version: ver,
			Active: active != "" && strings.Contains(ver, active),
		})
	}

	// Lock the running kernel, and the newest one that isn't running as the
	// fallback. "Newest" by natural order, not the order dpkg printed: dpkg
	// sorts as text, which puts -100 before -89.
	newestIdx := -1
	for i := range kernels {
		if kernels[i].Active {
			kernels[i].Locked = true
			continue
		}
		if newestIdx < 0 || naturalLess(kernels[newestIdx].Version, kernels[i].Version) {
			newestIdx = i
		}
	}
	if newestIdx >= 0 {
		kernels[newestIdx].Locked = true
	}
	return kernels
}

// hasVersionDigits reports whether a package suffix looks like a real kernel
// version rather than a meta-package name.
func hasVersionDigits(s string) bool {
	return strings.ContainsAny(s, "0123456789")
}

// naturalLess compares version-ish strings so that numeric runs compare
// numerically: 5.15.0-89 < 5.15.0-100, which plain string order gets backwards.
func naturalLess(a, b string) bool {
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		ad, bd := isDigit(a[ai]), isDigit(b[bi])
		if ad && bd {
			an, aNext := readNum(a, ai)
			bn, bNext := readNum(b, bi)
			if an != bn {
				return an < bn
			}
			ai, bi = aNext, bNext
			continue
		}
		if a[ai] != b[bi] {
			return a[ai] < b[bi]
		}
		ai++
		bi++
	}
	return len(a)-ai < len(b)-bi
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// readNum reads the digit run starting at i, returning its value and the index
// after it. Overflow falls back to 0, which only misorders absurd input.
func readNum(s string, i int) (int64, int) {
	j := i
	for j < len(s) && isDigit(s[j]) {
		j++
	}
	n, _ := strconv.ParseInt(s[i:j], 10, 64)
	return n, j
}

// removableKernels indexes the packages that may actually be purged. The UI
// disables the locked ones, but the UI is not a security boundary: this endpoint
// used to purge whatever package list it was handed, so a request naming
// "nginx" — or the running kernel — would have been carried out. Now it can only
// remove something this function returned.
func removableKernels(kernels []kernelInfo) map[string]bool {
	ok := map[string]bool{}
	for _, k := range kernels {
		if !k.Locked {
			ok[k.Package] = true
		}
	}
	return ok
}

// rejectedKernels returns the requested packages that removableKernels won't
// allow, so the refusal can name them instead of failing vaguely.
func rejectedKernels(requested []string, allowed map[string]bool) []string {
	var bad []string
	for _, p := range requested {
		if !allowed[p] {
			bad = append(bad, p)
		}
	}
	return bad
}
