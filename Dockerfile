# Stage 1: Build
# Use BUILDPLATFORM so the Go compiler runs natively (e.g. arm64 on Mac)
# and cross-compiles to the target arch via GOOS/GOARCH.
# For QEMU-emulated builds see Dockerfile.emulated.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.25-bookworm AS build

ARG TARGETOS TARGETARCH

WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

# Install the templ CLI at the version in go.mod: generated code must match
# the templ library, and @latest may need a newer Go than this image has.
RUN go install github.com/a-h/templ/cmd/templ@$(go list -m -f '{{.Version}}' github.com/a-h/templ)

# Copy source and generate + build.
COPY . .
RUN templ generate
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -o /examiner ./cmd/examiner/

# Stage 2: Runtime
FROM docker.io/library/debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN useradd -r -s /usr/sbin/nologin examiner

COPY --from=build /examiner /usr/local/bin/examiner

USER examiner
EXPOSE 8080

ENTRYPOINT ["examiner"]
CMD ["--addr", "0.0.0.0:8080"]
