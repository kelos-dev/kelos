FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY bin/kelos-controller .
USER 65532:65532
ENTRYPOINT ["/kelos-controller"]
