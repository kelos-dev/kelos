# Build stage
FROM golang:1.25 AS builder

WORKDIR /workspace

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o bin/kelos-token-refresher ./cmd/kelos-token-refresher

# Copy source
COPY . .

# Build
RUN make build WHAT=cmd/kelos-token-refresher

# Runtime stage
FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /workspace/bin/kelos-token-refresher .

USER 65532:65532

ENTRYPOINT ["/kelos-token-refresher"]
