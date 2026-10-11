# syntax=docker/dockerfile:1.7
# ── Console ────────────────────────────────────────────────────────────────
FROM node:25-alpine AS ui
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY ui/ ./
RUN npx tsc -b && npx vite build --outDir /out/dist --emptyOutDir

# ── Gateway ────────────────────────────────────────────────────────────────
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /out/dist ./web/dist
ARG VERSION=dev
RUN go build -ldflags="-s -w -X main.version=${VERSION}" -o /out/theophylax ./cmd/theophylax

# ── Runtime: distroless, non-root, read-only friendly ─────────────────────
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/theophylax /theophylax
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s CMD ["/theophylax", "healthcheck"]
ENTRYPOINT ["/theophylax"]
