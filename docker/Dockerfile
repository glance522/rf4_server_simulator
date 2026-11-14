FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY readme.go README.md ./
ARG TARGETOS TARGETARCH
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/ \
    ./cmd/seed ./cmd/gameserver ./cmd/gateway ./cmd/admin

FROM alpine:3.24
WORKDIR /app
RUN mkdir -p data certs && chown 10001:10001 data certs
COPY --from=build /out/ /usr/local/bin/
COPY server_data/static.sqlite3 server_data/compat_parts.json ./server_data/
COPY docker/config.yaml ./config.yaml
USER 10001:10001
