# ---- Build stage ----
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /tya \
      ./cmd/tya/cli

# ---- Runtime stage ----
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

# Create a non-root user
RUN addgroup -S tya && adduser -S tya -G tya

WORKDIR /workspace

COPY --from=builder /tya /usr/local/bin/tya

USER tya

ENTRYPOINT ["tya"]
CMD ["--help"]
