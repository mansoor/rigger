package api

import "testing"

// Verbatim `dpkg -l linux-image-*` on Ubuntu 22.04, trimmed to the package rows.
// Note the ordering: dpkg sorts as text, so -100 comes BEFORE -89 even though it
// is the newer kernel. That is the trap the natural-order comparison exists for.
const dpkgKernels = `ii  linux-image-5.15.0-100-generic 5.15.0-100.110 amd64 Signed kernel image generic
ii  linux-image-5.15.0-89-generic  5.15.0-89.99   amd64 Signed kernel image generic
ii  linux-image-5.15.0-91-generic  5.15.0-91.101  amd64 Signed kernel image generic
ii  linux-image-generic            5.15.0.100.97  amd64 Generic Linux kernel image
rc  linux-image-5.15.0-72-generic  5.15.0-72.79   amd64 Signed kernel image generic`

func lookup(t *testing.T, ks []kernelInfo, pkg string) kernelInfo {
	t.Helper()
	for _, k := range ks {
		if k.Package == pkg {
			return k
		}
	}
	t.Fatalf("kernel %q not in list", pkg)
	return kernelInfo{}
}

func TestParseKernels(t *testing.T) {
	ks := parseKernels(dpkgKernels, "5.15.0-91-generic")

	// The meta-package carries no image and purging it drags the real kernel
	// with it; the "rc" row is already removed and holds nothing to reclaim.
	if len(ks) != 3 {
		t.Fatalf("expected 3 real kernels, got %d: %+v", len(ks), ks)
	}
	for _, pkg := range []string{"linux-image-generic", "linux-image-5.15.0-72-generic"} {
		for _, k := range ks {
			if k.Package == pkg {
				t.Errorf("%s should not be listed", pkg)
			}
		}
	}

	if k := lookup(t, ks, "linux-image-5.15.0-91-generic"); !k.Active || !k.Locked {
		t.Errorf("running kernel must be active and locked, got %+v", k)
	}
	// The fallback must be the NEWEST non-running kernel. Text order would pick
	// -100 first anyway here, so the real assertion is that -89 stays free and
	// -100 is held — see TestParseKernelsNaturalOrder for the case text order
	// gets backwards.
	if k := lookup(t, ks, "linux-image-5.15.0-100-generic"); !k.Locked {
		t.Errorf("newest non-running kernel must be locked as the fallback, got %+v", k)
	}
	if k := lookup(t, ks, "linux-image-5.15.0-89-generic"); k.Locked {
		t.Errorf("an older spare kernel should be removable, got %+v", k)
	}
}

// The fallback is chosen by version, not by the order dpkg happened to print.
func TestParseKernelsNaturalOrder(t *testing.T) {
	const out = `ii  linux-image-5.15.0-100-generic 5.15.0-100.110 amd64 x
ii  linux-image-5.15.0-89-generic  5.15.0-89.99   amd64 x`
	// Neither is running — a rescue boot, say. The newer of the two must be kept.
	ks := parseKernels(out, "6.1.0-rescue")
	if k := lookup(t, ks, "linux-image-5.15.0-100-generic"); !k.Locked {
		t.Error("-100 is newer than -89 and must be the locked fallback")
	}
	if k := lookup(t, ks, "linux-image-5.15.0-89-generic"); k.Locked {
		t.Error("-89 is the older kernel and should be removable")
	}
}

// A machine with exactly one kernel must offer nothing: removing it leaves an
// unbootable server.
func TestParseKernelsSingleKernelIsLocked(t *testing.T) {
	const out = `ii  linux-image-5.15.0-91-generic 5.15.0-91.101 amd64 x`
	ks := parseKernels(out, "5.15.0-91-generic")
	if len(ks) != 1 || !ks[0].Locked {
		t.Fatalf("the only kernel must be locked, got %+v", ks)
	}
	if len(removableKernels(ks)) != 0 {
		t.Error("nothing should be removable")
	}
}

func TestNaturalLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"5.15.0-89-generic", "5.15.0-100-generic", true}, // the whole point
		{"5.15.0-100-generic", "5.15.0-89-generic", false},
		{"5.15.0-91-generic", "5.15.0-91-generic", false}, // equal is not less
		{"5.4.0-200-generic", "5.15.0-89-generic", true},  // 4 < 15
		{"5.15.0-89", "5.15.0-89-generic", true},          // prefix is shorter
	}
	for _, c := range cases {
		if got := naturalLess(c.a, c.b); got != c.want {
			t.Errorf("naturalLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// The purge endpoint must not act on a package the list never offered. Before
// this guard it ran `apt-get purge -y` on whatever names it was handed.
func TestRejectedKernels(t *testing.T) {
	ks := parseKernels(dpkgKernels, "5.15.0-91-generic")
	allowed := removableKernels(ks)

	if bad := rejectedKernels([]string{"linux-image-5.15.0-89-generic"}, allowed); len(bad) != 0 {
		t.Errorf("an unlocked kernel should be allowed, rejected %v", bad)
	}
	for _, pkg := range []string{
		"linux-image-5.15.0-91-generic",  // running
		"linux-image-5.15.0-100-generic", // fallback
		"linux-image-generic",            // meta-package, never listed
		"nginx",                          // not a kernel at all
		"",                               // empty
	} {
		if bad := rejectedKernels([]string{pkg}, allowed); len(bad) != 1 {
			t.Errorf("%q must be refused", pkg)
		}
	}
}
