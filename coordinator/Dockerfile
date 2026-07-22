# syntax=docker/dockerfile:1
#
# Requires BuildKit (the RUN --mount cache lines below). Docker 23+ enables it
# by default when the buildx plugin is present; install `docker-buildx` if a
# build fails with "the --mount option requires BuildKit".

# --- build stage ----------------------------------------------------------
FROM golang:1.24-alpine AS build

WORKDIR /src

# Copy manifests first: this layer stays cached until dependencies actually
# change, so editing Go sources does not re-download the module graph.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# The cache mounts persist the module cache and the compiler's build cache
# *across* builds, so a rebuild after a code edit recompiles only what changed
# instead of the whole dependency tree.
#
# CGO_ENABLED=0 produces a fully static binary, so the runtime image needs no
# libc. -trimpath strips local paths; -s -w drop the symbol table and DWARF.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -ldflags="-s -w" \
        -o /out/coordinator ./cmd/coordinator

# --- runtime stage --------------------------------------------------------
FROM alpine:3.20

# ca-certificates for outbound TLS; wget backs the container healthcheck.
RUN apk add --no-cache ca-certificates wget \
 && adduser -D -H -u 10001 coordinator

COPY --from=build /out/coordinator /usr/local/bin/coordinator

# Never run as root: a compromised process should not own the container.
USER coordinator

EXPOSE 8080

# Exec form, not shell: the binary becomes PID 1 and receives SIGTERM directly,
# which is what its graceful shutdown depends on.
ENTRYPOINT ["/usr/local/bin/coordinator"]
