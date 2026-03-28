FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY bin/kelos-webhook-receiver .
USER 65532:65532
ENTRYPOINT ["/kelos-webhook-receiver"]
