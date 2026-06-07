// Package keygen derives short, Docker-safe identifier keys from free-form
// display names, and resolves collisions within a configurable length budget.
//
// A key is the canonical identity for a workspace or project: it names the
// filesystem folder, the URL segment, and (via resource_prefix) every Docker
// artifact. It must therefore be lowercase [a-z0-9] — Docker Compose rejects
// uppercase project names. Display names stay free-form; keys are derived from
// them.
//
// Derivation distributes `min` characters across the name's words, front-loaded:
//
//	min=3  "web"           -> "web"          (1 word: first 3)
//	min=3  "acme corp"     -> "acc"          (2 words: 2 + 1)
//	min=3  "web app svc"   -> "was"          (3 words: 1 + 1 + 1)
//	min=3  "a b c d"       -> "abc"          (4+ words: first char of first 3)
//	min=5  "acme corp"     -> "acmco"        (2 words: 3 + 2)
package keygen

import "strings"

// suffixAlphabet is the collision-suffix order: digits 2-9 first (matching the
// "append a number" preference), then a-z, then 0/1 last (0 and 1 read poorly as
// the first suffix char).
const suffixAlphabet = "23456789abcdefghijklmnopqrstuvwxyz01"

// splitWords lowercases s and splits it into maximal [a-z0-9] runs. Separators
// (space, dash, underscore, any non-alphanumeric) are dropped. Case is the only
// signal we ignore — camelCase is not treated as a boundary, since display names
// use explicit separators.
func splitWords(s string) []string {
	var words []string
	var cur strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
		} else if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}

// Derive builds a base key of up to `min` characters from name. It may return a
// shorter key when the name has too few alphanumeric characters to reach `min`
// (the caller/UI should flag that the key needs extending). Returns "" only when
// the name has no alphanumeric content at all.
func Derive(name string, min int) string {
	if min < 1 {
		min = 1
	}
	words := splitWords(name)
	if len(words) == 0 {
		return ""
	}
	w := len(words)
	base := min / w
	rem := min % w

	consumed := make([]int, w)
	var b strings.Builder
	for i, word := range words {
		take := base
		if i < rem {
			take++
		}
		if take > len(word) {
			take = len(word)
		}
		b.WriteString(word[:take])
		consumed[i] = take
	}
	key := []byte(b.String())

	// Top-up: if some words were shorter than their allotment, pull additional
	// leading characters from words that still have some, in word order.
	for len(key) < min {
		progressed := false
		for i, word := range words {
			if consumed[i] < len(word) {
				key = append(key, word[consumed[i]])
				consumed[i]++
				progressed = true
				if len(key) >= min {
					break
				}
			}
		}
		if !progressed {
			break
		}
	}
	return string(key)
}

// clampMax truncates s to at most max characters.
func clampMax(s string, max int) string {
	if max > 0 && len(s) > max {
		return s[:max]
	}
	return s
}

// ResolveUnique returns a key derived from base that satisfies taken==false,
// staying within [min, max] characters. It first tries the base, then appends a
// single suffix char (if there is room), then (at max length) cycles the last
// char, then tries a two-char suffix. If everything in budget is exhausted it
// returns the last candidate tried (the caller validates and surfaces an error).
//
// taken reports whether a candidate key already exists in the relevant scope.
func ResolveUnique(base string, min, max int, taken func(string) bool) string {
	base = clampMax(base, max)
	if base == "" {
		base = "x"
	}
	if !taken(base) {
		return base
	}
	// A) Append one suffix char when there is room.
	if len(base) < max {
		for _, c := range suffixAlphabet {
			cand := base + string(c)
			if !taken(cand) {
				return cand
			}
		}
	}
	// B) Replace the last char (keeps length — works when base is already at max),
	//    as long as the stem stays within the minimum.
	if len(base) >= min && len(base) >= 1 {
		stem := base[:len(base)-1]
		for _, c := range suffixAlphabet {
			cand := stem + string(c)
			if cand != base && !taken(cand) {
				return cand
			}
		}
	}
	// C) Two-char suffix, if the budget allows.
	if len(base)+2 <= max {
		for _, c1 := range suffixAlphabet {
			for _, c2 := range suffixAlphabet {
				cand := base + string(c1) + string(c2)
				if !taken(cand) {
					return cand
				}
			}
		}
	}
	// Exhausted: return a best-effort candidate (caller re-validates).
	if len(base) < max {
		return base + "2"
	}
	return base
}

// Suggest is the common path: derive a base from name, then resolve collisions.
func Suggest(name string, min, max int, taken func(string) bool) string {
	return ResolveUnique(Derive(name, min), min, max, taken)
}

// Normalize sanitizes a user-supplied key override: lowercase and strip any
// character that is not [a-z0-9].
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Valid reports whether key is a well-formed key of length within [min, max].
// Keys are lowercase alphanumeric; Normalize already guarantees the charset, so
// this is the authoritative length+charset gate for overrides.
func Valid(key string, min, max int) bool {
	if len(key) < min || len(key) > max {
		return false
	}
	for _, r := range key {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}
