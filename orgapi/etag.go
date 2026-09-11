package orgapi

import (
	"strconv"
	"strings"
)

// ETag renders a registry version as the strong entity tag the registry
// emits: the decimal version in double quotes.
func ETag(version int64) string {
	return `"` + strconv.FormatInt(version, 10) + `"`
}

// ParseETag reads one entity tag back into a version. It accepts the quoted
// form the registry emits, the weak form (W/"n"), and — for the convenience of
// clients that just store the integer — bare digits.
func ParseETag(tag string) (int64, bool) {
	tag = strings.TrimSpace(tag)
	tag = strings.TrimPrefix(tag, "W/")
	tag = strings.Trim(tag, `"`)
	n, err := strconv.ParseInt(tag, 10, 64)
	return n, err == nil
}

// ETagMatches reports whether an If-None-Match header names version. The
// header is a comma-separated list of entity tags, each in any form ParseETag
// accepts; `*` matches any current version. This is the registry's own rule,
// shared so a fake server and a consumer's expectations cannot drift from it.
func ETagMatches(ifNoneMatch string, version int64) bool {
	for _, tag := range strings.Split(ifNoneMatch, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" {
			return true
		}
		if n, ok := ParseETag(tag); ok && n == version {
			return true
		}
	}
	return false
}
