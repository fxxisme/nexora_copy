package app

import "strings"

// Web image models use the ordinary ChatGPT Web image route. The route itself
// is selected as auto; the public model name is retained for compatibility and
// logging.
var webImageModels = []string{
	"gpt-image-2",
	"gpt-image-2.5",
	"gpt-image-2.5-flare",
	"gpt-image-2.5-sunburst",
}

func normalizeImageModel(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func isWebImageModel(model string) bool {
	slug := normalizeImageModel(model)
	for _, supported := range webImageModels {
		if slug == supported {
			return true
		}
	}
	return false
}

func isCodexImageModel(model string) bool {
	slug := normalizeImageModel(model)
	return slug == "codex-gpt-image-2" ||
		strings.HasSuffix(slug, "-codex-gpt-image-2")
}

func isSupportedImageModel(model string) bool {
	return isWebImageModel(model) || isCodexImageModel(model)
}
