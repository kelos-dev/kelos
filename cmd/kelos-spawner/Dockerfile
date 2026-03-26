FROM golang:1.25 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o bin/kelos-spawner ./cmd/kelos-spawner

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/bin/kelos-spawner .
USER 65532:65532
ENTRYPOINT ["/kelos-spawner"]
