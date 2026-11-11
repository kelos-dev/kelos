FROM gcr.io/distroless/static-debian13:nonroot
ARG TARGETARCH
WORKDIR /
COPY bin/kelos-webhook-server-linux-${TARGETARCH} kelos-webhook-server
USER 65532:65532
ENTRYPOINT ["/kelos-webhook-server"]
