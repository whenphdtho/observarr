# Base images are pinned by digest; the tag each digest belongs to is in the comment above it.

# Web app
# node:22-alpine
FROM node@sha256:1314fb30ec37592d5845e5a160d522c0e680972aa534549940b8d73bde88481a AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Binary (static, no cgo)
# golang:1.26-alpine
FROM golang@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /observarr ./cmd/observarr \
 && mkdir -p /rootfs/data && chmod 0700 /rootfs/data

# Runtime: no shell, no package manager, non-root (65532 is distroless's nonroot user)
# gcr.io/distroless/static-debian12:nonroot
FROM gcr.io/distroless/static-debian12@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /observarr /usr/local/bin/observarr
COPY --from=build --chown=65532:65532 /rootfs/ /
VOLUME /data
EXPOSE 9898
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/usr/local/bin/observarr", "-health"]
ENTRYPOINT ["/usr/local/bin/observarr"]
