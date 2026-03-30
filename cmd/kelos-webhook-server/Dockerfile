# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /workspace

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o webhook-server ./cmd/kelos-webhook-server

# Runtime stage
FROM gcr.io/distroless/static:nonroot

WORKDIR /

# Copy the binary
COPY --from=builder /workspace/webhook-server .

USER 65532:65532

ENTRYPOINT ["/webhook-server"]