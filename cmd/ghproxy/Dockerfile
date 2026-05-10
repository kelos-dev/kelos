FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH LDFLAGS=""
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "${LDFLAGS}" -o /out/ghproxy ./cmd/ghproxy

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /out/ghproxy ghproxy
USER 65532:65532
ENTRYPOINT ["/ghproxy"]
