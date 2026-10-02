package release

import (
	"strconv"
	"strings"
)

// VersionFile is the release asset that names the version, such as "v0.1.2". It is signed with the other files.
const VersionFile = "version.txt"

// ParseVersion reads v1.2.3 or 1.2.3. A suffix after a dash or plus sign is ignored. Anything else, such as
// the "dev" of a local build, is not a version.
func ParseVersion(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	var v [3]int
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// Compare returns -1, 0 or 1 when a is older than, equal to or newer than b. ok is false when either side
// is not a version.
func Compare(a, b string) (cmp int, ok bool) {
	x, okA := ParseVersion(a)
	y, okB := ParseVersion(b)
	if !okA || !okB {
		return 0, false
	}
	for i := range x {
		if x[i] < y[i] {
			return -1, true
		}
		if x[i] > y[i] {
			return 1, true
		}
	}
	return 0, true
}

// Newer reports whether candidate is a newer version than current. A build that is not a version, like
// "dev", is never offered an update.
func Newer(candidate, current string) bool {
	c, ok := Compare(candidate, current)
	return ok && c > 0
}
