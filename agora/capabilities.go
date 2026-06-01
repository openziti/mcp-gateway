package agora

import "strings"

// Derive concatenates and deduplicates capability names in first-seen order.
func Derive(base []string, extras []string) []string {
	seen := map[string]struct{}{}
	capabilities := make([]string, 0, len(base)+len(extras))

	for _, source := range [][]string{base, extras} {
		for _, capability := range source {
			capability = strings.TrimSpace(capability)
			if capability == "" {
				continue
			}
			if _, ok := seen[capability]; ok {
				continue
			}
			seen[capability] = struct{}{}
			capabilities = append(capabilities, capability)
		}
	}

	return capabilities
}
