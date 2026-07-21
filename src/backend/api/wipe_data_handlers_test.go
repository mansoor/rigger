package api

import "testing"

func TestConfirmSentences(t *testing.T) {
	if got := wipeConfirmSentence("shop", "dev"); got != "Wipe data for Project: shop Environment: dev" {
		t.Errorf("wipe sentence = %q", got)
	}
	if got := deleteConfirmSentence("shop"); got != "Delete Project: shop" {
		t.Errorf("delete sentence = %q", got)
	}
}

func TestEnvAllowedForWipe(t *testing.T) {
	allowed := []string{"dev", " Test "} // note the surrounding spaces + case
	cases := map[string]bool{"dev": true, "test": true, "prod": false, "staging": false, "": false}
	for env, want := range cases {
		if got := envAllowedForWipe(allowed, env); got != want {
			t.Errorf("envAllowedForWipe(%q) = %v, want %v", env, got, want)
		}
	}
}
