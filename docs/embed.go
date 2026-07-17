package projectdocs

import "embed"

// FS contains the documentation served to agents by the application.
//
//go:embed agents.md api.md llms.txt
var FS embed.FS
