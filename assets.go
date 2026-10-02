package donate

import "embed"

// Assets is the complete offline frontend shipped in every release.
//
//go:embed web
var Assets embed.FS
