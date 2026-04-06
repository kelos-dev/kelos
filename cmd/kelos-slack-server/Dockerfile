FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY bin/kelos-slack-server .
USER 65532:65532
ENTRYPOINT ["/kelos-slack-server"]
