# Distribution image for the apigw CLI.
#
# This image ships the static apigw binary in a minimal base. It is
# intended for CI/CD usage (driving a remote apigw install over SSH,
# running `apigw config validate` against checked-in YAML, generating
# OpenAPI specs from a pipeline, etc.) — NOT for running the actual
# gateway, which expects to install nginx + systemd directly on a host.
#
# For a full self-hosted gateway, use the OS package or `apigw install`
# on a real Debian/Ubuntu/Alpine host. This image is a tool, not a
# runtime.
#
# Build:    docker build -t apigw:dev .
# Run:      docker run --rm apigw:dev version
# Validate: docker run --rm -v $PWD:/work -w /work apigw:dev config validate install.yml

# ---- builder ----------------------------------------------------------------
FROM golang:1.25-alpine AS builder

WORKDIR /src

# Pre-cache modules to keep rebuilds fast.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath \
      -ldflags="-s -w \
        -X github.com/devevghenicernev-png/apigw/internal/build.Version=${VERSION} \
        -X github.com/devevghenicernev-png/apigw/internal/build.Commit=${COMMIT} \
        -X github.com/devevghenicernev-png/apigw/internal/build.Date=${DATE}" \
      -o /out/apigw ./cmd/apigw

# ---- runtime ----------------------------------------------------------------
# Alpine, not scratch/distroless: apigw shells out to git, curl, and ssh
# for the deploy/clone path; users expect those on the PATH when running
# `apigw deploy add ...` from CI. Adding ~8MB of base for those tools is
# worth it vs. surprising users with "exec: git not found".
FROM alpine:3.20

RUN apk add --no-cache \
      ca-certificates \
      git \
      openssh-client \
      curl \
      tzdata \
 && adduser -D -u 10001 -h /home/apigw apigw

COPY --from=builder /out/apigw /usr/local/bin/apigw

USER apigw
WORKDIR /home/apigw

ENTRYPOINT ["/usr/local/bin/apigw"]
CMD ["--help"]

LABEL org.opencontainers.image.title="apigw" \
      org.opencontainers.image.description="apigw CLI — install, manage, and observe an nginx-fronted API gateway" \
      org.opencontainers.image.source="https://github.com/devevghenicernev-png/apigw" \
      org.opencontainers.image.licenses="Apache-2.0"
