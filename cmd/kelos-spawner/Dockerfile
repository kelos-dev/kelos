FROM gcr.io/distroless/static-debian13:nonroot
ARG TARGETARCH
WORKDIR /
COPY bin/kelos-spawner-linux-${TARGETARCH} kelos-spawner
USER 65532:65532
ENTRYPOINT ["/kelos-spawner"]
