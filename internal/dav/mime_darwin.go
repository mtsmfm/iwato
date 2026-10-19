package dav

import "mime"

// macOS has no system MIME table that Go reads (Linux has
// /etc/mime.types, Windows its registry), so the types the editors and
// browsers care about beyond Go's own list are added here.
func init() {
	_ = mime.AddExtensionType(".md", "text/markdown; charset=utf-8")
}
