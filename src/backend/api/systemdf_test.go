package api

import "testing"

// Verbatim `docker system df` from Docker 29.6.1. Note the type name is one word
// for Images/Containers and two for Local Volumes/Build Cache, which shifts every
// column after it — the thing the old parser got wrong for Build Cache.
const systemDF = `TYPE            TOTAL     ACTIVE    SIZE      RECLAIMABLE
Images          41        27        14.27GB   3.18GB (22%)
Containers      37        37        23.72MB   0B (0%)
Local Volumes   78        24        14.28GB   5.109GB (35%)
Build Cache     182       31        3.086GB   1.758GB`

func TestParseSystemDF(t *testing.T) {
	d := parseSystemDF(systemDF)

	cases := []struct {
		name                 string
		got                  DiskSection
		count, size, reclaim int64
	}{
		{"images", d.Images, 41, parseDockerSize("14.27GB"), parseDockerSize("3.18GB")},
		{"containers", d.Containers, 37, parseDockerSize("23.72MB"), 0},
		{"volumes", d.Volumes, 78, parseDockerSize("14.28GB"), parseDockerSize("5.109GB")},
		// Regression: this row used to read its SIZE from the ACTIVE column, so a
		// 3 GB build cache reported 0 bytes and its 1.7 GB never counted as
		// reclaimable — the dashboard quietly under-reported reclaimable space.
		{"build cache", d.BuildCache, 182, parseDockerSize("3.086GB"), parseDockerSize("1.758GB")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got.Count != tc.count {
				t.Errorf("count = %d, want %d", tc.got.Count, tc.count)
			}
			if tc.got.SizeBytes != tc.size {
				t.Errorf("size = %d, want %d", tc.got.SizeBytes, tc.size)
			}
			if tc.got.ReclaimableBytes != tc.reclaim {
				t.Errorf("reclaimable = %d, want %d", tc.got.ReclaimableBytes, tc.reclaim)
			}
		})
	}

	// Every section must contribute, or a health verdict is computed from a
	// partial picture.
	size, recl := d.Total()
	if size <= d.Images.SizeBytes {
		t.Errorf("total size %d should exceed images alone", size)
	}
	if recl <= 0 {
		t.Error("total reclaimable should be non-zero for this fixture")
	}
}

func TestParseSystemDFTolerant(t *testing.T) {
	// Empty, header-only, and junk must all yield zeroes rather than panic —
	// a host whose docker speaks differently should degrade, not crash.
	for _, in := range []string{"", "TYPE TOTAL ACTIVE SIZE RECLAIMABLE", "garbage\nmore garbage", "Images"} {
		d := parseSystemDF(in)
		if size, _ := d.Total(); size != 0 {
			t.Errorf("parseSystemDF(%q) should be empty, got size %d", in, size)
		}
	}
}

// A daemon reporting plain zeroes must not be mistaken for an unparseable one.
func TestParseSystemDFAllZero(t *testing.T) {
	const clean = `TYPE            TOTAL     ACTIVE    SIZE      RECLAIMABLE
Images          0         0         0B        0B
Containers      0         0         0B        0B
Local Volumes   0         0         0B        0B
Build Cache     0         0         0B        0B`
	d := parseSystemDF(clean)
	if size, recl := d.Total(); size != 0 || recl != 0 {
		t.Fatalf("expected zeroes, got size=%d reclaimable=%d", size, recl)
	}
}
