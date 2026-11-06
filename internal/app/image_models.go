package app

import "strings"

// Web image models use the ordinary ChatGPT Web image route. The route itself
// is selected as auto (or specific slug if supported); the public model name is
// retained for compatibility and logging.
var webImageModels = []string{
	"gpt-image-2",
	"gpt-image-2.5",
	"gpt-image-2.5-flare",
	"gpt-image-2.5-sunburst",
	"gpt-image-2.6",
	"gpt-image-2.6-flare",
	"gpt-image-2.6-sunburst",
}

func normalizeImageModel(model string) string {
	slug := strings.ToLower(strings.TrimSpace(model))
	switch slug {
	case "image-2":
		return "gpt-image-2"
	case "image-2.5":
		return "gpt-image-2.5"
	case "image-2.5-flare":
		return "gpt-image-2.5-flare"
	case "image-2.5-sunburst":
		return "gpt-image-2.5-sunburst"
	case "image-2.6":
		return "gpt-image-2.6"
	case "image-2.6-flare":
		return "gpt-image-2.6-flare"
	case "image-2.6-sunburst":
		return "gpt-image-2.6-sunburst"
	}
	return slug
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
		strings.HasSuffix(slug, "-codex-gpt-image-2") ||
		slug == "codex-gpt-image-2.6" ||
		strings.HasSuffix(slug, "-codex-gpt-image-2.6")
}

func isSupportedImageModel(model string) bool {
	return isWebImageModel(model) || isCodexImageModel(model)
}
