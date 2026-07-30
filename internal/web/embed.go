package web

import "embed"

// dist は Vite でビルドしたフロントエンド (web/ → pnpm build で生成)。
//
//go:embed all:dist
var distFS embed.FS
