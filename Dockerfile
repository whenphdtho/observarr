# Base images are pinned by digest (of the multi-arch index); the tag each digest belongs to is in the
# comment above it. The web app and the Go binary are built on the build machine's own platform and the
# binary is cross-compiled for the target one, so a multi-arch build never runs them under emulation.

# Web app
# node:26-alpine
FROM --platform=$BUILDPLATFORM node@sha256:e18c3ccdf7d71619990b0e85639f506e2028a95410999cab24cc373cbd10e0ac AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Binary (static, no cgo)
# golang:1.26.11-alpine
FROM --platform=$BUILDPLATFORM golang@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/web/dist ./web/dist
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /observarr ./cmd/observarr \
 && mkdir -p /rootfs/data && chmod 0700 /rootfs/data

# Runtime: no shell, no package manager, non-root (65532 is distroless's nonroot user)
# gcr.io/distroless/static-debian13:nonroot
FROM gcr.io/distroless/static-debian13@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
COPY --from=build /observarr /usr/local/bin/observarr
COPY --from=build --chown=65532:65532 /rootfs/ /
VOLUME /data
EXPOSE 9898
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/usr/local/bin/observarr", "-health"]
ENTRYPOINT ["/usr/local/bin/observarr"]
