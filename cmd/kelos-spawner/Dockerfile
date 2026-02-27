FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY bin/kelos-spawner .
USER 65532:65532
ENTRYPOINT ["/kelos-spawner"]
