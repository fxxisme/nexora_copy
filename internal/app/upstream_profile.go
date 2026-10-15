package app

import (
	"os"
	"strings"

	"github.com/bogdanfinn/tls-client/profiles"
)

func upstreamTLSProfile() profiles.ClientProfile {
	name := strings.ToLower(strings.TrimSpace(os.Getenv("CHATGPT2API_TLS_PROFILE")))
	if name == "" {
		return profiles.Chrome_146
	}
	if profile, ok := profiles.MappedTLSClients[name]; ok {
		return profile
	}
	normalized := strings.ReplaceAll(name, "-", "_")
	if profile, ok := profiles.MappedTLSClients[normalized]; ok {
		return profile
	}
	return profiles.Chrome_146
}

func upstreamUserAgent() string {
	if ua := strings.TrimSpace(os.Getenv("CHATGPT2API_USER_AGENT")); ua != "" {
		return ua
	}
	return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
}

func upstreamSecCHUA() string {
	if chua := strings.TrimSpace(os.Getenv("CHATGPT2API_SEC_CH_UA")); chua != "" {
		return chua
	}
	profile := strings.ToLower(strings.TrimSpace(os.Getenv("CHATGPT2API_TLS_PROFILE")))
	version := "146"
	for _, candidate := range []string{"146", "144", "143", "133", "131", "124", "120", "117", "112", "111", "110", "109", "108", "107", "106", "105", "104", "103"} {
		if strings.Contains(profile, candidate) {
			version = candidate
			break
		}
	}
	return `"Chromium";v="` + version + `", "Google Chrome";v="` + version + `", "Not A(Brand";v="99"`
}
