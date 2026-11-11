FROM gcr.io/distroless/static-debian13:nonroot
ARG TARGETARCH
WORKDIR /
COPY bin/kelos-worker-runner-linux-${TARGETARCH} kelos-worker-runner
USER 61100:61100
ENTRYPOINT ["/kelos-worker-runner"]
