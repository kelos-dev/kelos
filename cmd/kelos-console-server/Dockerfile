FROM gcr.io/distroless/static-debian13:nonroot
ARG TARGETARCH
WORKDIR /
COPY bin/kelos-console-server-linux-${TARGETARCH} kelos-console-server
USER 65532:65532
ENTRYPOINT ["/kelos-console-server"]
