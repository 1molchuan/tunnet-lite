package control

import (
	"encoding/json"
	"strconv"
	"strings"
)

// MinimumVersion reads the client version floor the control plane advertises.
//
// The directory is withheld from clients below this floor: the response then
// carries only the release block, with no runtime section. Reading the floor
// from the response is what lets this client keep working when the vendor
// raises it, instead of failing with a confusing "no runtime section".
func MinimumVersion(payload []byte) string {
	var doc struct {
		Release struct {
			MinimumVersion string `json:"minimum_version"`
		} `json:"release"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return ""
	}
	return doc.Release.MinimumVersion
}

// versionLess reports whether a sorts before b, comparing dot-separated numeric
// components. Non-numeric components compare as zero, which is deliberate: a
// malformed version should not be treated as newer than a real one.
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		av, bv := component(as, i), component(bs, i)
		if av != bv {
			return av < bv
		}
	}
	return false
}

func component(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(parts[i]))
	if err != nil {
		return 0
	}
	return v
}
