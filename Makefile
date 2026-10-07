# loods: Go server with the Svelte UI embedded from web/dist.

BIN := $(HOME)/.local/bin/loods

.PHONY: build web install test dev clean

build: web
	go build -o loods .

web:
	cd web && npm install --silent && npm run build

install: build
	install -m 0755 loods $(BIN)

test:
	go test ./...
	cd web && npm run check

# UI with hot reload on :5173; run `go run . --no-open` alongside it.
dev:
	cd web && npm run dev

clean:
	rm -rf loods web/dist
