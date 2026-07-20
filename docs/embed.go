package projectdocs

import "embed"

// FS contains the worker contract served by the application.
//
//go:embed worker-contract.md
var FS embed.FS
