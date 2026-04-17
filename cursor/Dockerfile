FROM ubuntu:24.04

ARG GO_VERSION=1.25.0

RUN apt-get update && apt-get install -y \
    make \
    curl \
    ca-certificates \
    git \
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y nodejs \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
       -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
       > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update \
    && apt-get install -y gh \
    && rm -rf /var/lib/apt/lists/*

RUN ARCH=$(dpkg --print-architecture) \
    && TARBALL="go${GO_VERSION}.linux-${ARCH}.tar.gz" \
    && curl -fsSL -o "/tmp/${TARBALL}" "https://dl.google.com/go/${TARBALL}" \
    && curl -fsSL -o "/tmp/${TARBALL}.sha256" "https://dl.google.com/go/${TARBALL}.sha256" \
    && echo "$(cat /tmp/${TARBALL}.sha256)  /tmp/${TARBALL}" | sha256sum -c - \
    && tar -C /usr/local -xzf "/tmp/${TARBALL}" \
    && rm "/tmp/${TARBALL}" "/tmp/${TARBALL}.sha256"

ENV PATH="/usr/local/go/bin:${PATH}"

COPY cursor/kelos_entrypoint.sh /kelos_entrypoint.sh
RUN chmod +x /kelos_entrypoint.sh

ARG TARGETARCH
COPY bin/kelos-capture-linux-${TARGETARCH} /kelos/kelos-capture

RUN useradd -u 61100 -m -s /bin/bash agent
RUN mkdir -p /home/agent/.cursor && chown -R agent:agent /home/agent

USER agent

ARG CURSOR_CLI_VERSION=2026.04.15-dccdccd
RUN ARCH=$(uname -m) \
    && case "$ARCH" in x86_64) ARCH="x64" ;; aarch64) ARCH="arm64" ;; esac \
    && INSTALL_DIR="$HOME/.local/share/cursor-agent/versions/${CURSOR_CLI_VERSION}" \
    && mkdir -p "$INSTALL_DIR" "$HOME/.local/bin" \
    && curl -fsSL "https://downloads.cursor.com/lab/${CURSOR_CLI_VERSION}/linux/${ARCH}/agent-cli-package.tar.gz" \
       | tar --strip-components=1 -xz -C "$INSTALL_DIR" \
    && ln -sf "$INSTALL_DIR/cursor-agent" "$HOME/.local/bin/agent" \
    && ln -sf "$INSTALL_DIR/cursor-agent" "$HOME/.local/bin/cursor-agent"

ENV GOPATH="/home/agent/go"
ENV PATH="/home/agent/.local/bin:${GOPATH}/bin:${PATH}"

WORKDIR /workspace

ENTRYPOINT ["agent"]
