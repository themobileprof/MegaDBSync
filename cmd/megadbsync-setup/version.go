//go:build windows

package main

import (
	"strconv"
	"strings"
)

func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

func compareVersions(a, b string) int {
	pa := versionParts(normalizeVersion(a))
	pb := versionParts(normalizeVersion(b))
	for i := 0; i < 3; i++ {
		ai, bi := 0, 0
		if i < len(pa) {
			ai = pa[i]
		}
		if i < len(pb) {
			bi = pb[i]
		}
		if ai != bi {
			return ai - bi
		}
	}
	return strings.Compare(normalizeVersion(a), normalizeVersion(b))
}

func versionParts(v string) []int {
	if v == "" || v == "dev" {
		return nil
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

func versionLess(a, b string) bool {
	return compareVersions(a, b) < 0
}

func versionEqual(a, b string) bool {
	return compareVersions(a, b) == 0
}
