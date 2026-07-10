package executor

import "testing"

func TestBinOr(t *testing.T) {
	if got := BinOr(""); got != "docker" {
		t.Errorf("BinOr(\"\") = %q, want docker", got)
	}
	if got := BinOr("nixpacks"); got != "nixpacks" {
		t.Errorf("BinOr(nixpacks) = %q, want nixpacks", got)
	}
}

func TestNewCmdHonorsBin(t *testing.T) {
	// Default (empty Bin) → docker; existing call sites are unchanged.
	cmd, cancel := newCmd(Spec{Args: []string{"ps"}})
	defer cancel()
	if cmd.Args[0] != "docker" {
		t.Errorf("default bin = %q, want docker", cmd.Args[0])
	}
	// Explicit Bin runs that binary instead.
	cmd2, cancel2 := newCmd(Spec{Bin: "nixpacks", Args: []string{"build"}})
	defer cancel2()
	if cmd2.Args[0] != "nixpacks" {
		t.Errorf("bin = %q, want nixpacks", cmd2.Args[0])
	}
}
