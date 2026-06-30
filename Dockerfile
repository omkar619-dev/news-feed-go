# ─────────────────────────────────────────────────────────────
# Stage 1: BUILDER — has the full Go toolchain. Compiles the binaries.
# ─────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

WORKDIR /src

# Copy ONLY the dependency manifests first, download deps, THEN copy the source.
# Docker caches each layer: as long as go.mod/go.sum don't change, the (slow)
# dependency download is reused and skipped on later builds — only re-running
# when deps actually change. Big build-speed win.
COPY go.mod go.sum ./
RUN go mod download

# Now copy the rest of the source.
COPY . .

# Build BOTH binaries from the one codebase.
#   CGO_ENABLED=0 → a fully static binary (no libc), so it runs on a minimal base.
#   GOOS=linux    → target Linux (matters when building from a non-Linux host).
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/api    ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/worker ./cmd/worker
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/relay  ./cmd/relay
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/ssh    ./cmd/ssh

# ─────────────────────────────────────────────────────────────
# Stage 2: RUNTIME — tiny. Carries ONLY the compiled binaries.
# ─────────────────────────────────────────────────────────────
FROM alpine:3.20

# ca-certificates: lets the app make outbound HTTPS calls if it ever needs to.
# Then create a non-root user (security: never run as root in a container).
RUN apk add --no-cache ca-certificates && adduser -D -u 1000 appuser

# Copy the two binaries from the builder stage. Nothing else comes across —
# no Go compiler, no source code → small image, smaller attack surface.
COPY --from=builder /out/api    /app/api
COPY --from=builder /out/worker /app/worker
COPY --from=builder /out/relay  /app/relay
COPY --from=builder /out/ssh    /app/ssh

USER appuser

EXPOSE 3000

# Default command = the API. The WORKER deployment overrides this with
# ["/app/worker"] — SAME image, two roles, chosen at run time.
ENTRYPOINT ["/app/api"]
