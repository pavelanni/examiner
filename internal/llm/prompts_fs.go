package llm

import "embed"

//go:embed prompts/*.txt
var promptsFS embed.FS
