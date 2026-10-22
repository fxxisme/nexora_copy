package app

import (
	"sync"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type WebDAVClient struct {
	URL      string
	Username string
	Password string
	RootPath string
	client   *http.Client
	dirCache sync.Map
}

func NewWebDAVClient(serverURL, username, password, rootPath string) *WebDAVClient {
	return &WebDAVClient{
		URL:      strings.TrimRight(strings.TrimSpace(serverURL), "/"),
		Username: strings.TrimSpace(username),
		Password: strings.TrimSpace(password),
		RootPath: strings.Trim(strings.TrimSpace(rootPath), "/"),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *WebDAVClient) buildURL(rel string) string {
	parts := []string{}
	if c.RootPath != "" {
		for _, seg := range strings.Split(c.RootPath, "/") {
			if seg != "" {
				parts = append(parts, url.PathEscape(seg))
			}
		}
	}
	cleanedRel := strings.Trim(strings.TrimSpace(rel), "/")
	if cleanedRel != "" {
		for _, seg := range strings.Split(cleanedRel, "/") {
			if seg != "" {
				parts = append(parts, url.PathEscape(seg))
			}
		}
	}
	if len(parts) == 0 {
		return c.URL
	}
	return c.URL + "/" + strings.Join(parts, "/")
}

func (c *WebDAVClient) setAuth(req *http.Request) {
	if c.Username != "" || c.Password != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
}

func (c *WebDAVClient) EnsureDirs(ctx context.Context, rel string) error {
	dir := path.Dir(strings.Trim(rel, "/"))
	if dir == "." || dir == "/" || dir == "" {
		dir = ""
	}
	parts := []string{}
	if c.RootPath != "" {
		for _, seg := range strings.Split(c.RootPath, "/") {
			if seg != "" {
				parts = append(parts, seg)
			}
		}
	}
	if dir != "" {
		for _, seg := range strings.Split(dir, "/") {
			if seg != "" {
				parts = append(parts, seg)
			}
		}
	}

	curr := c.URL
	for _, seg := range parts {
		curr += "/" + url.PathEscape(seg)
		if _, ok := c.dirCache.Load(curr); ok {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, "MKCOL", curr, nil)
		if err != nil {
			return err
		}
		c.setAuth(req)
		resp, err := c.client.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		// 201 Created; 405 Method Not Allowed (通常表示目录已存在); 200~299 成功
		if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusMethodNotAllowed || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
			c.dirCache.Store(curr, struct{}{})
		} else if resp.StatusCode >= 400 {
			return fmt.Errorf("WebDAV MKCOL %s failed: status %d", curr, resp.StatusCode)
		}
	}
	return nil
}

func (c *WebDAVClient) Put(ctx context.Context, rel string, data []byte, contentType string) error {
	if err := c.EnsureDirs(ctx, rel); err != nil {
		return err
	}
	targetURL := c.buildURL(rel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, targetURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	c.setAuth(req)
	if contentType == "" {
		contentType = "image/png"
	}
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = int64(len(data))

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("WebDAV PUT failed: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *WebDAVClient) GetStream(ctx context.Context, rel string) (io.ReadCloser, int64, string, error) {
	targetURL := c.buildURL(rel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, 0, "", err
	}
	c.setAuth(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, 0, "", errors.New("image not found on WebDAV")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, 0, "", fmt.Errorf("WebDAV GET failed: HTTP %d", resp.StatusCode)
	}
	return resp.Body, resp.ContentLength, resp.Header.Get("Content-Type"), nil
}

func (c *WebDAVClient) Get(ctx context.Context, rel string) ([]byte, error) {
	body, _, _, err := c.GetStream(ctx, rel)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}

func (c *WebDAVClient) Delete(ctx context.Context, rel string) error {
	targetURL := c.buildURL(rel)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, targetURL, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return nil
	}
	return fmt.Errorf("WebDAV DELETE failed: HTTP %d", resp.StatusCode)
}

func (c *WebDAVClient) Test(ctx context.Context) (map[string]any, error) {
	if c.URL == "" {
		return map[string]any{"ok": false, "error": "WebDAV URL 不能为空"}, errors.New("WebDAV URL 不能为空")
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return map[string]any{"ok": false, "error": "无效的 WebDAV URL，必须以 http:// 或 https:// 开头"}, errors.New("无效的 WebDAV URL")
	}

	testRel := ".nexora_webdav_test.txt"
	testContent := []byte("nexora webdav test ok\n")

	if err := c.Put(ctx, testRel, testContent, "text/plain"); err != nil {
		return map[string]any{"ok": false, "error": fmt.Sprintf("写入测试失败: %v", err)}, err
	}
	_ = c.Delete(ctx, testRel)

	return map[string]any{"ok": true, "status": 200, "error": nil}, nil
}
