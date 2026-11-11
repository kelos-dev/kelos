FROM gcr.io/distroless/static-debian13:nonroot
ARG TARGETARCH
WORKDIR /
COPY bin/ghproxy-linux-${TARGETARCH} ghproxy
USER 65532:65532
ENTRYPOINT ["/ghproxy"]
