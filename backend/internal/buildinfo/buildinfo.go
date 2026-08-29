// Package buildinfo formats build metadata shared by the ao and hao binaries.
package buildinfo

import "strings"

// String renders build metadata as "<version> commit <c> built <d>", omitting
// unset commit and date fields.
func String(version, commit, date string) string {
	parts := []string{version}
	if commit != "" {
		parts = append(parts, "commit "+commit)
	}
	if date != "" {
		parts = append(parts, "built "+date)
	}
	return strings.Join(parts, " ")
}
