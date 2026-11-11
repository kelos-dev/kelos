FROM gcr.io/distroless/static-debian13:nonroot
ARG TARGETARCH
WORKDIR /
COPY bin/kelos-slack-server-linux-${TARGETARCH} kelos-slack-server
USER 65532:65532
ENTRYPOINT ["/kelos-slack-server"]
