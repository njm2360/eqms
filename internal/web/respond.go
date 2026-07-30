package web

import (
	"compress/gzip"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
)

func internalError(w http.ResponseWriter, what string, err error) {
	log.Printf("web: %s: %v", what, err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	writeJSONStatus(w, r, http.StatusOK, v)
}

func writeJSONStatus(w http.ResponseWriter, r *http.Request, code int, v any) {
	// ヘッダを送る前に JSON にする。ストリームへ直接書くと失敗を伝えられない
	body, err := json.Marshal(v)
	if err != nil {
		log.Printf("web: marshal %T: %v", v, err)
		http.Error(w, "encoding failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Vary", "Accept-Encoding")
	if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		w.WriteHeader(code)
		w.Write(body)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.WriteHeader(code)
	gz := gzip.NewWriter(w)
	defer gz.Close()
	gz.Write(body)
}

// acceptsGzip は Accept-Encoding を解釈する。gzip の明示指定が * より優先される。
func acceptsGzip(header string) bool {
	star, starOK := false, false
	for part := range strings.SplitSeq(header, ",") {
		token, params, _ := strings.Cut(part, ";")
		switch t := strings.TrimSpace(token); {
		case strings.EqualFold(t, "gzip"):
			return qNonZero(params)
		case t == "*":
			star, starOK = true, qNonZero(params)
		}
	}
	return star && starOK
}

// qNonZero は q=0 でなければ true を返す。q=0 は明示的な拒否。
func qNonZero(params string) bool {
	for p := range strings.SplitSeq(params, ";") {
		k, v, found := strings.Cut(p, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return err == nil && q > 0
	}
	return true
}
