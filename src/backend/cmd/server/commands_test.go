package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/mansoor/rigger/ui/internal/buildinfo"
)

func TestRunCommand(t *testing.T) {
	for _, c := range []struct {
		args   []string
		code   int
		out    string
		errOut string
	}{
		{[]string{"help"}, 0, "With no command, rigger starts the server", ""},
		{[]string{"--help"}, 0, "Usage: rigger", ""},
		{[]string{"version"}, 0, buildinfo.Version, ""},
		{[]string{"frobnicate"}, 2, "", `unknown command "frobnicate"`},
		{[]string{"user", "list"}, 2, "", `unknown command "user"`},
		{[]string{"--port", "8080"}, 2, "", `unknown command "--port"`},
		{[]string{""}, 2, "", `unknown command ""`},
	} {
		var out, errOut bytes.Buffer
		code := runCommand(c.args, &out, &errOut)
		if code != c.code || !strings.Contains(out.String(), c.out) || !strings.Contains(errOut.String(), c.errOut) {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", c.args, code, out.String(), errOut.String())
		}
		if c.code == 2 && !strings.Contains(errOut.String(), "Commands:") {
			t.Errorf("%q: a refused command should show the list of commands", c.args)
		}
	}
}

// An unknown command must fail before touching anything — it used to start a
// second server, which opened and migrated the database first.
func TestUnknownCommandLeavesTheDataDirAlone(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	var out, errOut bytes.Buffer
	if code := runCommand([]string{"user", "list"}, &out, &errOut); code != 2 {
		t.Fatalf("exit %d", code)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("an unknown command wrote to the data dir: %v", entries)
	}
}
