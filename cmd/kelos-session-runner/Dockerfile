FROM gcr.io/distroless/static:nonroot
ARG TARGETARCH
WORKDIR /
COPY bin/kelos-session-runner-linux-${TARGETARCH} kelos-session-runner
USER 61100:61100
ENTRYPOINT ["/kelos-session-runner"]
