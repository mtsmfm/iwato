// Package agentsmd is the standard image's generated /AGENTS.md: a link
// to Generated, which holds the image's text (Static) and, after Marker, a
// section Iwato writes (the sandbox's URLs). iwato-entrypoint writes it at
// start with the section it already had; Iwato rewrites the whole file.
package agentsmd

import "bytes"

const (
	Static = "/etc/iwato/AGENTS.md"
	// Directly in the home: a directory on the way (a dotfiles link,
	// another owner) could keep the writers out and leave /AGENTS.md
	// dangling.
	GeneratedRel = ".iwato-agents.md"
	Generated    = "/home/dev/" + GeneratedRel
	Marker       = "<!-- iwato: generated section -->"
)

// Compose is the image's text followed by old's generated section.
func Compose(static, old []byte) []byte {
	out := bytes.Clone(static)
	if i := bytes.Index(old, []byte("\n"+Marker)); i >= 0 {
		out = append(out, old[i:]...)
	}
	return out
}
