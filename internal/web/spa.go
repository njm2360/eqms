package web

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

// spaHandler は未知のパスを index.html にフォールバックする。
func spaHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic(err)
	}
	// embed の ModTime は 0 で Last-Modified が出ないため、内容から ETag を作る
	sum := sha256.Sum256(index)
	etag := fmt.Sprintf(`"%x"`, sum[:8])
	fileServer := http.FileServerFS(sub)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			if _, err := fs.Stat(sub, r.URL.Path[1:]); err == nil {
				if strings.HasPrefix(r.URL.Path, "/assets/") {
					// ファイル名に内容ハッシュが入るので中身が変われば URL も変わる
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
}
