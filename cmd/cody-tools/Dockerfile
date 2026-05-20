FROM gcr.io/distroless/static:nonroot
ARG TARGETARCH
WORKDIR /
COPY bin/cody-tools-linux-${TARGETARCH} cody-tools
USER 65532:65532
ENTRYPOINT ["/cody-tools"]
