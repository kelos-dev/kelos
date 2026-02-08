FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY bin/axon-spawner .
USER 65532:65532
ENTRYPOINT ["/axon-spawner"]
