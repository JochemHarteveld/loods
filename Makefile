# loods: Go server with the Svelte UI embedded from web/dist.
#
# The Go code lives in cmd/loods (the entry point) and internal/* (one package
# per concern); web/ is the Svelte app plus the embed that puts dist/ in the
# binary, which is why `web` runs before any Go build.

BIN := $(HOME)/.local/bin/loods

.PHONY: build web install test dev clean

build: web
	go build -o loods ./cmd/loods

web:
	cd web && npm install --silent && npm run build

install: build
	install -m 0755 loods $(BIN)

test:
	go test ./...
	cd web && npm run check

# UI with hot reload on :5173; run `go run ./cmd/loods --no-open` alongside it.
dev:
	cd web && npm run dev

clean:
	rm -rf loods web/dist
