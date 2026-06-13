package envorder

import (
	"reflect"
	"testing"
)

func TestResolveAutoGuess(t *testing.T) {
	got := Resolve([]string{"prod", "dev", "staging"}, nil, nil)
	want := []string{"dev", "staging", "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("auto-guess = %v, want %v", got, want)
	}
}

func TestResolveExplicitWins(t *testing.T) {
	// Explicit order is honoured; an env missing from it (staging) is appended.
	got := Resolve([]string{"dev", "staging", "prod"}, []string{"prod", "dev"}, nil)
	want := []string{"prod", "dev", "staging"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("explicit = %v, want %v", got, want)
	}
}

func TestResolveExplicitDropsStaleNames(t *testing.T) {
	// A name in explicit that no longer exists is ignored; surviving envs only.
	got := Resolve([]string{"dev", "prod"}, []string{"prod", "gone", "dev"}, nil)
	want := []string{"prod", "dev"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stale-name = %v, want %v", got, want)
	}
}

func TestResolveUnknownEnvSortsLast(t *testing.T) {
	got := Resolve([]string{"dev", "zeta", "prod"}, nil, nil)
	want := []string{"dev", "prod", "zeta"} // zeta matches no tier → last
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unknown-last = %v, want %v", got, want)
	}
}

func TestResolvePreprodBeforeProd(t *testing.T) {
	got := Resolve([]string{"prod", "preprod", "dev"}, nil, nil)
	want := []string{"dev", "preprod", "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("preprod-order = %v, want %v", got, want)
	}
}

func TestResolveCustomTierNames(t *testing.T) {
	// Custom tier list overrides the default guess.
	tiers := SplitTierNames("alpha, beta, gamma")
	got := Resolve([]string{"gamma-1", "alpha-1", "beta-1"}, nil, tiers)
	want := []string{"alpha-1", "beta-1", "gamma-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("custom-tiers = %v, want %v", got, want)
	}
}

func TestSplitTierNames(t *testing.T) {
	if got := SplitTierNames(""); !reflect.DeepEqual(got, DefaultTiers) {
		t.Errorf("empty should return DefaultTiers, got %v", got)
	}
	if got := SplitTierNames("  "); !reflect.DeepEqual(got, DefaultTiers) {
		t.Errorf("blank should return DefaultTiers, got %v", got)
	}
	got := SplitTierNames("Dev, Stage\nProd\n")
	want := []string{"dev", "stage", "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("split = %v, want %v", got, want)
	}
}
