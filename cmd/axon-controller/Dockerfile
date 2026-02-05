# Build stage
FROM golang:1.25 AS builder

WORKDIR /workspace

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build
RUN make build WHAT=cmd/axon-controller

# Runtime stage
FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /workspace/bin/axon-controller .

USER 65532:65532

ENTRYPOINT ["/axon-controller"]
