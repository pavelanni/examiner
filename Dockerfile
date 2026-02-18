# Stage 1: Build
FROM docker.io/library/golang:1.25-bookworm AS build

# Install templ CLI.
RUN go install github.com/a-h/templ/cmd/templ@latest

WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and generate + build.
COPY . .
RUN templ generate
RUN CGO_ENABLED=0 go build -o /examiner ./cmd/examiner/

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
