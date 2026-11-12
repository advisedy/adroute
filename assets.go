package adroute

import (
	"embed"
	"io/fs"
)

// EmbeddedFiles contains the frontend and shipped adapter examples.
//
//go:embed public examples docs/repair-agent-prompt.md
var embedded embed.FS

func EmbeddedFiles() fs.FS { return embedded }
