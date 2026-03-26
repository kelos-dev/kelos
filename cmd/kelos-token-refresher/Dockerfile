FROM golang:1.25 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o bin/kelos-token-refresher ./cmd/kelos-token-refresher

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/bin/kelos-token-refresher .
USER 65532:65532
ENTRYPOINT ["/kelos-token-refresher"]
