package app

import (
	"bytes"
	"image"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
)

func (s *Server) thumbnailPath(rel string) string {
	return filepath.Join(s.dataDir, "image_thumbnails", filepath.FromSlash(rel)+".jpg")
}

func (s *Server) generateThumbnailFromData(rel string, data []byte) {
	rel, err := safeImageRel(rel)
	if err != nil {
		return
	}
	thumb := s.thumbnailPath(rel)
	if _, err := os.Stat(thumb); err == nil {
		return
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return
	}
	bounds := img.Bounds()
	w0, h0 := bounds.Dx(), bounds.Dy()
	if w0 <= 0 || h0 <= 0 {
		return
	}
	maxSide := 360
	nw, nh := w0, h0
	if w0 >= h0 && w0 > maxSide {
		nw = maxSide
		nh = h0 * maxSide / w0
	} else if h0 > w0 && h0 > maxSide {
		nh = maxSide
		nw = w0 * maxSide / h0
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			sx := bounds.Min.X + x*w0/nw
			sy := bounds.Min.Y + y*h0/nh
			dst.Set(x, y, img.At(sx, sy))
		}
	}
	_ = os.MkdirAll(filepath.Dir(thumb), 0755)
	out, err := os.Create(thumb)
	if err != nil {
		return
	}
	defer out.Close()
	_ = jpeg.Encode(out, dst, &jpeg.Options{Quality: 82})
}

func (s *Server) serveThumbnail(w http.ResponseWriter, r *http.Request, rel string) {
	rel, err := safeImageRel(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	thumb := s.thumbnailPath(rel)
	if _, err := os.Stat(thumb); err == nil {
		http.ServeFile(w, r, thumb)
		return
	}

	// 1. 本地原图生成缩略图
	src, err := s.imagePath(rel)
	if err == nil {
		if f, err := os.Open(src); err == nil {
			defer f.Close()
			if img, _, err := image.Decode(f); err == nil {
				s.encodeAndServeThumbnail(w, r, thumb, img, src)
				return
			}
		}
	}

	// 2. 本地不存在，尝试从 WebDAV 回源拉取生成缩略图
	client := s.webdavClient()
	if client != nil {
		data, err := client.Get(r.Context(), rel)
		if err == nil && len(data) > 0 {
			if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
				s.encodeAndServeThumbnail(w, r, thumb, img, "")
				return
			}
		}
	}

	http.NotFound(w, r)
}

func (s *Server) encodeAndServeThumbnail(w http.ResponseWriter, r *http.Request, thumb string, img image.Image, fallbackSrc string) {
	bounds := img.Bounds()
	w0, h0 := bounds.Dx(), bounds.Dy()
	if w0 <= 0 || h0 <= 0 {
		if fallbackSrc != "" {
			http.ServeFile(w, r, fallbackSrc)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	maxSide := 360
	nw, nh := w0, h0
	if w0 >= h0 && w0 > maxSide {
		nw = maxSide
		nh = h0 * maxSide / w0
	} else if h0 > w0 && h0 > maxSide {
		nh = maxSide
		nw = w0 * maxSide / h0
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			sx := bounds.Min.X + x*w0/nw
			sy := bounds.Min.Y + y*h0/nh
			dst.Set(x, y, img.At(sx, sy))
		}
	}
	_ = os.MkdirAll(filepath.Dir(thumb), 0755)
	out, err := os.Create(thumb)
	if err != nil {
		if fallbackSrc != "" {
			http.ServeFile(w, r, fallbackSrc)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	defer out.Close()
	_ = jpeg.Encode(out, dst, &jpeg.Options{Quality: 82})
	http.ServeFile(w, r, thumb)
}
