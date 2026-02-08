FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY bin/axon-controller .
USER 65532:65532
ENTRYPOINT ["/axon-controller"]
