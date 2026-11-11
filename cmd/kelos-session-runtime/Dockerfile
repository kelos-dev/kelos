FROM gcr.io/distroless/static-debian13:nonroot
ARG TARGETARCH
WORKDIR /
COPY bin/kelos-session-runtime-linux-${TARGETARCH} kelos-session-runtime
USER 61100:61100
ENTRYPOINT ["/kelos-session-runtime"]
