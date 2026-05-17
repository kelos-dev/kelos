FROM ubuntu:24.04

ARG GO_VERSION=1.25.0

RUN apt-get update && apt-get install -y \
    make \
    curl \
    ca-certificates \
    git \
    jq \
    openssl \
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y nodejs \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
       -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
       > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update \
    && apt-get install -y gh \
    && rm -rf /var/lib/apt/lists/*

# kubectl — pinned to a stable minor matching what the cluster runs.
# Tasks that need cluster introspection use this against the in-cluster
# API server via the mounted ServiceAccount token.
ARG KUBECTL_VERSION=v1.31.0
RUN ARCH=$(dpkg --print-architecture) \
    && curl -fsSL -o /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${ARCH}/kubectl" \
    && curl -fsSL -o /tmp/kubectl.sha256 "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${ARCH}/kubectl.sha256" \
    && echo "$(cat /tmp/kubectl.sha256)  /usr/local/bin/kubectl" | sha256sum -c - \
    && chmod +x /usr/local/bin/kubectl \
    && rm /tmp/kubectl.sha256

RUN ARCH=$(dpkg --print-architecture) \
    && TARBALL="go${GO_VERSION}.linux-${ARCH}.tar.gz" \
    && curl -fsSL -o "/tmp/${TARBALL}" "https://dl.google.com/go/${TARBALL}" \
    && curl -fsSL -o "/tmp/${TARBALL}.sha256" "https://dl.google.com/go/${TARBALL}.sha256" \
    && echo "$(cat /tmp/${TARBALL}.sha256)  /tmp/${TARBALL}" | sha256sum -c - \
    && tar -C /usr/local -xzf "/tmp/${TARBALL}" \
    && rm "/tmp/${TARBALL}" "/tmp/${TARBALL}.sha256"

ENV PATH="/usr/local/go/bin:${PATH}"

ARG CODEX_VERSION=0.130.0
RUN npm install -g @openai/codex@${CODEX_VERSION}

COPY codex/kelos_entrypoint.sh /kelos_entrypoint.sh
RUN chmod +x /kelos_entrypoint.sh

# Helper scripts for tasks that need git push + cluster read access. Each
# is a no-op when the corresponding env vars are unset, so adding them
# costs nothing for Q&A-style tasks that don't mount the github-app
# Secret or the ServiceAccount token. See codex/scripts/README for the
# env contract.
COPY codex/scripts/github-app-credential-helper /usr/local/bin/github-app-credential-helper
COPY codex/scripts/github-app-token /usr/local/bin/github-app-token
COPY codex/scripts/kelos-agent-setup /usr/local/bin/kelos-agent-setup
RUN chmod +x /usr/local/bin/github-app-credential-helper \
    /usr/local/bin/github-app-token \
    /usr/local/bin/kelos-agent-setup

ARG TARGETARCH
COPY bin/kelos-capture-linux-${TARGETARCH} /kelos/kelos-capture

RUN useradd -u 61100 -m -s /bin/bash agent
RUN mkdir -p /home/agent/.codex && chown -R agent:agent /home/agent

USER agent

ENV GOPATH="/home/agent/go"
ENV PATH="${GOPATH}/bin:${PATH}"

WORKDIR /workspace

ENTRYPOINT ["codex"]
