FROM golang:1.25 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o bin/kelos-controller ./cmd/kelos-controller

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/bin/kelos-controller .
USER 65532:65532
ENTRYPOINT ["/kelos-controller"]
