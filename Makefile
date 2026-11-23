.PHONY: ui check build

ui:
	cd web && npm ci && npm run build && touch dist/.gitkeep

check:
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...
	go test -race ./...

build: ui
	CGO_ENABLED=0 go build -trimpath -o observarr ./cmd/observarr
