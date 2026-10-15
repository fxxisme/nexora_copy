package app

import (
	"os"
	"strings"

	tlsclient "github.com/bogdanfinn/tls-client"
)

func newUpstreamHTTPClient(proxyURL string, binGetter func() (string, error)) (upstreamHTTPDoer, error) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CHATGPT2API_UPSTREAM_TRANSPORT")), "curl-impersonate") {
		return newCurlImpersonateClient(proxyURL, binGetter)
	}
	options := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(upstreamTLSProfile()),
		tlsclient.WithCookieJar(tlsclient.NewCookieJar()),
		tlsclient.WithTimeoutSeconds(300),
		tlsclient.WithNotFollowRedirects(),
	}
	if strings.TrimSpace(proxyURL) != "" {
		options = append(options, tlsclient.WithProxyUrl(strings.TrimSpace(proxyURL)))
	}
	return tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
}
