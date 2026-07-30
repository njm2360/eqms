# フロントエンドを internal/web/dist へビルドしてから Go に焼き込む
.PHONY: build web test dev-server dev-web clean

build: web
	go build -o eqms ./cmd/eqms

web:
	cd web && pnpm install && pnpm build

test:
	go test ./...

# 開発時: dev-server と dev-web を別ターミナルで起動する
dev-server:
	EQMS_SIM=1 go run ./cmd/eqms

dev-web:
	cd web && pnpm dev

clean:
	rm -rf eqms internal/web/dist
