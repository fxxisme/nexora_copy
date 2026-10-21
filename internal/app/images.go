package app

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Server) webdavClient() *WebDAVClient {
	if strings.TrimSpace(s.cfg.WebDAVURL) == "" {
		return nil
	}
	return NewWebDAVClient(s.cfg.WebDAVURL, s.cfg.WebDAVUsername, s.cfg.WebDAVPassword, s.cfg.WebDAVRootPath)
}

func (s *Server) saveImage(r *http.Request, data []byte) (string, string, error) {
	return s.saveImageWithBaseURL(s.baseURL(r), data)
}

func (s *Server) saveImageWithBaseURL(baseURL string, data []byte) (string, string, error) {
	s.maybeCleanupOldImages()
	sum := md5.Sum(data)
	relDir := filepath.Join(time.Now().Format("2006"), time.Now().Format("01"), time.Now().Format("02"))
	name := fmt.Sprintf("%d_%x.png", time.Now().Unix(), sum)
	rel := filepath.ToSlash(filepath.Join(relDir, name))

	mode := strings.ToLower(strings.TrimSpace(s.cfg.ImageStorageMode))
	if mode == "" {
		mode = "local"
	}
	client := s.webdavClient()
	if (mode == "webdav" || mode == "both") && client == nil {
		mode = "local"
	}

	// 1. 本地存储
	storedLocal := false
	if mode == "local" || mode == "both" {
		path := filepath.Join(s.imagesDir, relDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return "", "", err
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			return "", "", err
		}
		storedLocal = true
	}

	// 2. WebDAV 远端存储
	storedWebDAV := false
	if mode == "webdav" || mode == "both" {
		if err := client.Put(context.Background(), rel, data, "image/png"); err != nil {
			if mode == "webdav" && !storedLocal {
				return "", "", fmt.Errorf("WebDAV 上传失败: %w", err)
			}
			if s.logSvc != nil {
				s.logSvc.add("system", "WebDAV图片备份失败", map[string]any{"rel": rel, "error": err.Error()})
			}
		} else {
			storedWebDAV = true
		}
	}

	// 3. 生成本地缩略图（几十KB，极速加载画廊且不占存储空间）
	s.generateThumbnailFromData(rel, data)

	// 4. 记录图片元数据
	actualStorage := "local"
	if storedLocal && storedWebDAV {
		actualStorage = "both"
	} else if storedWebDAV {
		actualStorage = "webdav"
	}
	_ = s.store.UpdateImageMeta(func(m map[string]ImageMeta) map[string]ImageMeta {
		if m == nil {
			m = map[string]ImageMeta{}
		}
		m[rel] = ImageMeta{
			Rel:       rel,
			Name:      name,
			Date:      time.Now().Format("2006-01-02"),
			Size:      int64(len(data)),
			CreatedAt: time.Now().Format(time.RFC3339),
			Storage:   actualStorage,
		}
		return m
	})

	imageURL := baseURL + "/images/" + rel
	if strings.TrimSpace(s.cfg.ImageStoragePublicBaseURL) != "" {
		imageURL = strings.TrimRight(strings.TrimSpace(s.cfg.ImageStoragePublicBaseURL), "/") + "/" + rel
	}

	return rel, imageURL, nil
}

func (s *Server) recordOwner(id *Identity, rel string) {
	if id == nil {
		return
	}
	_ = s.store.UpdateOwners(func(owners map[string]string) map[string]string {
		owners[rel] = id.ID
		return owners
	})
}

func (s *Server) recordPrompt(rel, prompt string, isEdit bool) {
	_ = s.store.UpdatePrompts(func(ps map[string]map[string]any) map[string]map[string]any {
		ps[rel] = map[string]any{"prompt": prompt, "is_edit": isEdit, "created_at": time.Now().Unix()}
		return ps
	})
}

func (s *Server) maybeCleanupOldImages() {
	s.imageCleanupMu.Lock()
	if time.Since(s.lastImageCleanup) < time.Hour {
		s.imageCleanupMu.Unlock()
		return
	}
	s.lastImageCleanup = time.Now()
	s.imageCleanupMu.Unlock()
	s.cleanupOldImages()
}

func (s *Server) cleanupOldImages() int {
	days := s.cfg.ImageRetentionDays
	if days <= 0 {
		days = 30
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	protected := map[string]bool{}
	if s.cfg.CleanupProtectGallery {
		for _, it := range s.store.LoadGallery() {
			if rel := relClean(it.ImageRel); rel != "" {
				protected[rel] = true
			}
		}
	}
	if s.cfg.CleanupProtectUserImages {
		for rel, owner := range s.store.LoadOwners() {
			owner = strings.ToLower(strings.TrimSpace(owner))
			if rel = relClean(rel); rel != "" && owner != "" && owner != "admin" && owner != "__admin__" {
				protected[rel] = true
			}
		}
	}
	removed := 0
	_ = filepath.WalkDir(s.imagesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		st, err := d.Info()
		if err != nil || st.ModTime().After(cutoff) {
			return nil
		}
		rel, err := filepath.Rel(s.imagesDir, path)
		if err == nil && protected[filepath.ToSlash(rel)] {
			return nil
		}
		if os.Remove(path) == nil {
			removed++
		}
		return nil
	})
	dirs := []string{}
	_ = filepath.WalkDir(s.imagesDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != s.imagesDir {
			dirs = append(dirs, path)
		}
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		_ = os.Remove(d)
	}
	if removed > 0 && s.logSvc != nil {
		s.logSvc.add("system", "清理旧图片", map[string]any{"removed": removed, "retention_days": days})
	}
	return removed
}

func relFromURL(u string) string {
	if i := strings.Index(u, "/images/"); i >= 0 {
		return relClean(u[i+8:])
	}
	return relClean(u)
}

func safeImageRel(value string) (string, error) {
	rel := relClean(value)
	if rel == "" {
		return "", fmt.Errorf("image path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("invalid image path")
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if cleaned == "." || cleaned == "" || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("invalid image path")
	}
	return cleaned, nil
}

func (s *Server) imagePath(rel string) (string, error) {
	cleaned, err := safeImageRel(rel)
	if err != nil {
		return "", err
	}
	base := filepath.Clean(s.imagesDir)
	target := filepath.Clean(filepath.Join(base, filepath.FromSlash(cleaned)))
	if target != base && !strings.HasPrefix(target, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid image path")
	}
	return target, nil
}

func (s *Server) handleServeImage(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/images/")
	cleanedRel, err := safeImageRel(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// 1. 本地文件优先
	localPath, err := s.imagePath(cleanedRel)
	if err == nil {
		if st, err := os.Stat(localPath); err == nil && !st.IsDir() {
			http.ServeFile(w, r, localPath)
			return
		}
	}

	// 2. 本地不存在时尝试从 WebDAV 回源读取
	client := s.webdavClient()
	if client != nil {
		stream, size, contentType, err := client.GetStream(r.Context(), cleanedRel)
		if err == nil {
			defer stream.Close()
			if contentType != "" {
				w.Header().Set("Content-Type", contentType)
			} else {
				w.Header().Set("Content-Type", "image/png")
			}
			if size > 0 {
				w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
			}
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = io.Copy(w, stream)
			return
		}
	}

	http.NotFound(w, r)
}

func (s *Server) listImages(r *http.Request, ownerFilter string) map[string]any {
	owners := s.store.LoadOwners()
	prompts := s.store.LoadPrompts()
	tags := s.store.LoadTags()
	metaMap := s.store.LoadImageMeta()

	itemsMap := map[string]map[string]any{}

	// 1. 加载元数据索引（涵盖保存在 WebDAV 或两端的图片）
	for rel, m := range metaMap {
		owner := owners[rel]
		if ownerFilter != "" {
			if ownerFilter == "__unowned__" && owner != "" {
				continue
			}
			if ownerFilter != "__unowned__" && owner != ownerFilter {
				continue
			}
		}
		pr := prompts[rel]
		imgURL := s.baseURL(r) + "/images/" + rel
		if strings.TrimSpace(s.cfg.ImageStoragePublicBaseURL) != "" {
			imgURL = strings.TrimRight(strings.TrimSpace(s.cfg.ImageStoragePublicBaseURL), "/") + "/" + rel
		}
		storage := m.Storage
		if storage == "" {
			storage = "local"
		}
		itemsMap[rel] = map[string]any{
			"rel":           rel,
			"path":          rel,
			"name":          m.Name,
			"date":          m.Date,
			"size":          m.Size,
			"url":           imgURL,
			"thumbnail_url": s.baseURL(r) + "/image-thumbnails/" + rel,
			"created_at":    m.CreatedAt,
			"tags":          tags[rel],
			"owner_id":      owner,
			"prompt":        strAny(pr["prompt"], ""),
			"storage":       storage,
		}
	}

	// 2. 扫描合并本地历史未入索引的图片
	_ = filepath.WalkDir(s.imagesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".webp" {
			return nil
		}
		rel, _ := filepath.Rel(s.imagesDir, path)
		rel = filepath.ToSlash(rel)
		if _, exists := itemsMap[rel]; exists {
			return nil
		}
		owner := owners[rel]
		if ownerFilter != "" {
			if ownerFilter == "__unowned__" && owner != "" {
				return nil
			}
			if ownerFilter != "__unowned__" && owner != ownerFilter {
				return nil
			}
		}
		st, _ := d.Info()
		pr := prompts[rel]
		imgURL := s.baseURL(r) + "/images/" + rel
		if strings.TrimSpace(s.cfg.ImageStoragePublicBaseURL) != "" {
			imgURL = strings.TrimRight(strings.TrimSpace(s.cfg.ImageStoragePublicBaseURL), "/") + "/" + rel
		}
		itemsMap[rel] = map[string]any{
			"rel":           rel,
			"path":          rel,
			"name":          d.Name(),
			"date":          st.ModTime().Format("2006-01-02"),
			"size":          st.Size(),
			"url":           imgURL,
			"thumbnail_url": s.baseURL(r) + "/image-thumbnails/" + rel,
			"created_at":    st.ModTime().Format(time.RFC3339),
			"tags":          tags[rel],
			"owner_id":      owner,
			"prompt":        strAny(pr["prompt"], ""),
			"storage":       "local",
		}
		return nil
	})

	items := make([]map[string]any, 0, len(itemsMap))
	for _, it := range itemsMap {
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return strAny(items[i]["created_at"], "") > strAny(items[j]["created_at"], "") })
	return map[string]any{"items": items}
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, 200, s.listImages(r, r.URL.Query().Get("owner")))
}

func (s *Server) handleMyImages(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	owner := id.ID
	if id.Role == "admin" {
		owner = "admin"
	}
	writeJSON(w, 200, s.listImages(r, owner))
}

func (s *Server) handleImageOwners(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	owners := s.store.LoadOwners()
	counts := map[string]int{}
	for _, o := range owners {
		counts[o]++
	}
	items := []map[string]any{{"id": "__admin__", "name": "管理员", "deleted": false, "count": counts["admin"]}, {"id": "__unowned__", "name": "未归属", "deleted": false, "count": 0}}
	for _, k := range s.store.LoadAuthKeys() {
		if k.Role == "user" {
			items = append(items, map[string]any{"id": k.ID, "name": k.Name, "deleted": false, "count": counts[k.ID]})
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) handleImageDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	var b struct {
		Paths []string `json:"paths"`
	}
	if !readBody(w, r, &b) {
		return
	}
	owners := s.store.LoadOwners()
	metaMap := s.store.LoadImageMeta()
	prompts := s.store.LoadPrompts()
	tags := s.store.LoadTags()
	client := s.webdavClient()

	removed := 0
	for _, p := range b.Paths {
		rel, err := safeImageRel(p)
		if err != nil {
			continue
		}
		if id.Role != "admin" && owners[rel] != id.ID {
			continue
		}

		deletedThis := false
		// 1. 删除本地原图（如果存在）
		if localPath, err := s.imagePath(rel); err == nil {
			if os.Remove(localPath) == nil {
				deletedThis = true
			}
		}
		// 2. 删除本地缩略图
		_ = os.Remove(s.thumbnailPath(rel))

		// 3. 删除 WebDAV 远端（如果存在）
		if client != nil {
			if err := client.Delete(r.Context(), rel); err == nil {
				deletedThis = true
			}
		}

		if deletedThis || metaMap[rel].Rel != "" {
			removed++
			delete(owners, rel)
			delete(metaMap, rel)
			delete(prompts, rel)
			delete(tags, rel)
		}
	}

	_ = s.store.SaveOwners(owners)
	_ = s.store.SaveImageMeta(metaMap)
	_ = s.store.SavePrompts(prompts)
	_ = s.store.SaveTags(tags)
	writeJSON(w, 200, map[string]any{"removed": removed})
}

func (s *Server) getImageData(ctx context.Context, rel string) ([]byte, error) {
	if localPath, err := s.imagePath(rel); err == nil {
		if data, err := os.ReadFile(localPath); err == nil {
			return data, nil
		}
	}
	if client := s.webdavClient(); client != nil {
		return client.Get(ctx, rel)
	}
	return nil, fmt.Errorf("image not found: %s", rel)
}

func (s *Server) handleImageDownload(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var b struct {
		Paths []string `json:"paths"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range b.Paths {
		rel, err := safeImageRel(p)
		if err != nil {
			continue
		}
		data, err := s.getImageData(r.Context(), rel)
		if err == nil {
			f, _ := zw.Create(filepath.Base(rel))
			_, _ = f.Write(data)
		}
	}
	_ = zw.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=images.zip")
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) handleImageDownloadSingle(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	rel, err := safeImageRel(strings.TrimPrefix(r.URL.Path, "/api/images/download/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// 1. 本地有直接服务
	if localPath, err := s.imagePath(rel); err == nil {
		if st, err := os.Stat(localPath); err == nil && !st.IsDir() {
			http.ServeFile(w, r, localPath)
			return
		}
	}

	// 2. 本地无则从 WebDAV 获取流
	if client := s.webdavClient(); client != nil {
		stream, size, contentType, err := client.GetStream(r.Context(), rel)
		if err == nil {
			defer stream.Close()
			if contentType != "" {
				w.Header().Set("Content-Type", contentType)
			} else {
				w.Header().Set("Content-Type", "image/png")
			}
			if size > 0 {
				w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
			}
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filepath.Base(rel)))
			_, _ = io.Copy(w, stream)
			return
		}
	}

	http.NotFound(w, r)
}

func (s *Server) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/image-thumbnails/")
	s.serveThumbnail(w, r, rel)
}

func (s *Server) handleImageTags(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	tags := s.store.LoadTags()
	if r.Method == http.MethodGet {
		set := map[string]bool{}
		for _, ts := range tags {
			for _, t := range ts {
				set[t] = true
			}
		}
		arr := []string{}
		for t := range set {
			arr = append(arr, t)
		}
		sort.Strings(arr)
		writeJSON(w, 200, map[string]any{"tags": arr})
		return
	}
	if r.Method == http.MethodPost {
		var b struct {
			Path string   `json:"path"`
			Tags []string `json:"tags"`
		}
		if !readBody(w, r, &b) {
			return
		}
		tags[relClean(b.Path)] = b.Tags
		_ = s.store.SaveTags(tags)
		writeJSON(w, 200, map[string]any{"ok": true, "tags": b.Tags})
		return
	}
	writeErr(w, 405, "method not allowed")
}

func (s *Server) handleImageTagDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	tag, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/images/tags/"))
	tags := s.store.LoadTags()
	n := 0
	for rel, ts := range tags {
		out := []string{}
		for _, t := range ts {
			if t == tag {
				n++
			} else {
				out = append(out, t)
			}
		}
		tags[rel] = out
	}
	_ = s.store.SaveTags(tags)
	writeJSON(w, 200, map[string]any{"ok": true, "removed_from": n})
}
