// Package buildinfo carries the Rigger build version, injected at link time so
// the running binary knows which release it is (for the in-app updater + "About").
//
// Set at build time with:
//
//	-ldflags "-X github.com/mansoor/rigger/ui/internal/buildinfo.Version=v1.2.3 \
//	          -X github.com/mansoor/rigger/ui/internal/buildinfo.Commit=abc1234"
//
// A plain `go build` (local source / dev) leaves the defaults below.
package buildinfo

// Version is the release version (e.g. "v1.2.3"), or "dev" for a source build.
var Version = "dev"

// Commit is the short git SHA the image was built from (optional; "" when unset).
var Commit = ""
