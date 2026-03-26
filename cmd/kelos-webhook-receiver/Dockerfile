FROM golang:1.25 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o bin/kelos-webhook-receiver ./cmd/kelos-webhook-receiver

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/bin/kelos-webhook-receiver .
USER 65532:65532
ENTRYPOINT ["/kelos-webhook-receiver"]
