package app

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const maxProxyImportLines = 10000
const maxProxyImportTextBytes = 1000000
const maxProxyImportBodyBytes = 8 << 20
const defaultProxyImportImageConcurrency = 30

type proxyNodeImportRequest struct {
	Text         string   `json:"text"`
	ExistingURLs []string `json:"existing_urls"`
}

type proxyNodeImportNode struct {
	URL                   string `json:"url"`
	ImageConcurrencyLimit int    `json:"image_concurrency_limit"`
}

type proxyNodeImportInvalidItem struct {
	Line   int    `json:"line"`
	Raw    string `json:"raw"`
	Reason string `json:"reason"`
}

type proxyNodeImportResult struct {
	Nodes          []proxyNodeImportNode        `json:"nodes"`
	AddedCount     int                          `json:"added_count"`
	DuplicateCount int                          `json:"duplicate_count"`
	InvalidCount   int                          `json:"invalid_count"`
	InvalidItems   []proxyNodeImportInvalidItem `json:"invalid_items"`
}

func normalizeProxyNodeImportURL(raw string) (string, error) {
	normalized, err := normalizeProxyURL(raw)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", errors.New("代理端口格式无效")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil {
			return "", errors.New("代理端口格式无效")
		}
		port = strconv.Itoa(value)
	}
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	return u.String(), nil
}

func parseProxyNodeImport(input proxyNodeImportRequest) (proxyNodeImportResult, error) {
	result := proxyNodeImportResult{Nodes: []proxyNodeImportNode{}, InvalidItems: []proxyNodeImportInvalidItem{}}
	if len(input.Text) > maxProxyImportTextBytes {
		return result, errors.New("代理节点文本不能超过 1000000 字节")
	}
	if len(input.ExistingURLs) > maxProxyImportLines {
		return result, errors.New("已有代理节点不能超过 10000 个")
	}
	existing := make(map[string]bool, len(input.ExistingURLs))
	for _, raw := range input.ExistingURLs {
		if normalized, err := normalizeProxyNodeImportURL(raw); err == nil {
			existing[normalized] = true
		}
	}
	lines := strings.Split(input.Text, "\n")
	seen := map[string]bool{}
	count := 0
	for index, line := range lines {
		raw := strings.TrimSpace(line)
		if raw == "" {
			continue
		}
		count++
		if count > maxProxyImportLines {
			return proxyNodeImportResult{}, errors.New("一次最多导入 10000 个代理节点")
		}
		parts := strings.Fields(raw)
		if len(parts) > 2 {
			result.InvalidItems = append(result.InvalidItems, proxyNodeImportInvalidItem{Line: index + 1, Raw: raw, Reason: "格式应为：代理地址 [图片并发]"})
			continue
		}
		limit := defaultProxyImportImageConcurrency
		if len(parts) == 2 {
			value := parts[1]
			valid := value != ""
			for _, digit := range value {
				if digit < '0' || digit > '9' {
					valid = false
					break
				}
			}
			if valid {
				var err error
				limit, err = strconv.Atoi(value)
				valid = err == nil
			}
			if !valid || limit < 0 || limit > 10000 {
				result.InvalidItems = append(result.InvalidItems, proxyNodeImportInvalidItem{Line: index + 1, Raw: raw, Reason: "图片并发必须是 0 到 10000 的整数"})
				continue
			}
		}
		normalized, err := normalizeProxyNodeImportURL(parts[0])
		if err != nil {
			result.InvalidItems = append(result.InvalidItems, proxyNodeImportInvalidItem{Line: index + 1, Raw: raw, Reason: err.Error()})
			continue
		}
		if existing[normalized] || seen[normalized] {
			result.DuplicateCount++
			continue
		}
		seen[normalized] = true
		result.Nodes = append(result.Nodes, proxyNodeImportNode{URL: normalized, ImageConcurrencyLimit: limit})
	}
	result.AddedCount = len(result.Nodes)
	result.InvalidCount = len(result.InvalidItems)
	return result, nil
}

func (s *Server) handleProxyNodeImport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxProxyImportBodyBytes)
	var input proxyNodeImportRequest
	if !readBody(w, r, &input) {
		return
	}
	result, err := parseProxyNodeImport(input)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
