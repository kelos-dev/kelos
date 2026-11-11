FROM gcr.io/distroless/static-debian13:nonroot
ARG TARGETARCH
WORKDIR /
COPY bin/kelos-controller-linux-${TARGETARCH} kelos-controller
USER 65532:65532
ENTRYPOINT ["/kelos-controller"]
