FROM ubuntu:24.04

ARG GO_VERSION=1.25.0

RUN apt-get update && apt-get install -y \
    make \
    curl \
    ca-certificates \
    git \
    jq \
    openssl \
    postgresql-client \
    redis-tools \
    dnsutils \
    netcat-openbsd \
    iputils-ping \
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y nodejs \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
       -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
       > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update \
    && apt-get install -y gh \
    && rm -rf /var/lib/apt/lists/*

# yq — YAML processor for k8s manifest introspection.
ARG YQ_VERSION=v4.45.1
RUN ARCH=$(dpkg --print-architecture) \
    && curl -fsSL -o /usr/local/bin/yq "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${ARCH}" \
    && chmod +x /usr/local/bin/yq

# kustomize — render gitops overlays locally during debug runs.
ARG KUSTOMIZE_VERSION=v5.5.0
RUN ARCH=$(dpkg --print-architecture) \
    && curl -fsSL -o /tmp/kustomize.tgz "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize/${KUSTOMIZE_VERSION}/kustomize_${KUSTOMIZE_VERSION}_linux_${ARCH}.tar.gz" \
    && tar -xzf /tmp/kustomize.tgz -C /usr/local/bin \
    && chmod +x /usr/local/bin/kustomize \
    && rm /tmp/kustomize.tgz

# Flux CLI — inspect HelmRelease / Kustomization / GitRepository state.
ARG FLUX_VERSION=2.4.0
RUN ARCH=$(dpkg --print-architecture) \
    && curl -fsSL -o /tmp/flux.tgz "https://github.com/fluxcd/flux2/releases/download/v${FLUX_VERSION}/flux_${FLUX_VERSION}_linux_${ARCH}.tar.gz" \
    && tar -xzf /tmp/flux.tgz -C /usr/local/bin \
    && chmod +x /usr/local/bin/flux \
    && rm /tmp/flux.tgz

# Helm — render and inspect charts (pinned to v3; Flux v2 HelmReleases and
# the existing chart corpus target helm 3). Bumping to v4 needs a sweep of
# the charts the agent might touch.
ARG HELM_VERSION=v3.21.0
RUN ARCH=$(dpkg --print-architecture) \
    && curl -fsSL -o /tmp/helm.tgz "https://get.helm.sh/helm-${HELM_VERSION}-linux-${ARCH}.tar.gz" \
    && tar -xzf /tmp/helm.tgz -C /tmp \
    && mv /tmp/linux-${ARCH}/helm /usr/local/bin/helm \
    && chmod +x /usr/local/bin/helm \
    && rm -rf /tmp/helm.tgz /tmp/linux-${ARCH}

# grpcurl — call Connect RPC / gRPC services directly (hermes-api, facade,
# etc.) for live API debugging. The kelos-curl JWT wrapper doesn't cover
# grpcurl, so the agent uses `kelos-jwt sign <service>` and passes the
# token via -H "Authorization: Bearer ...".
ARG GRPCURL_VERSION=1.9.3
RUN ARCH=$(dpkg --print-architecture) \
    && case "$ARCH" in amd64) GRPC_ARCH=x86_64 ;; arm64) GRPC_ARCH=arm64 ;; esac \
    && curl -fsSL -o /tmp/grpcurl.tgz "https://github.com/fullstorydev/grpcurl/releases/download/v${GRPCURL_VERSION}/grpcurl_${GRPCURL_VERSION}_linux_${GRPC_ARCH}.tar.gz" \
    && tar -xzf /tmp/grpcurl.tgz -C /usr/local/bin grpcurl \
    && chmod +x /usr/local/bin/grpcurl \
    && rm /tmp/grpcurl.tgz

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

# mise — required by platform-services' Makefile / pre-PR checks
# (mise run fmt:all / lint:all / build:all / test:all). Platform-services
# pins min_version=2025.2.6 in mise.toml; pin a recent stable release.
ARG MISE_VERSION=2025.5.0
RUN ARCH=$(uname -m | sed 's/x86_64/x64/;s/aarch64/arm64/') \
    && curl -fsSL -o /tmp/mise.tgz "https://github.com/jdx/mise/releases/download/v${MISE_VERSION}/mise-v${MISE_VERSION}-linux-${ARCH}.tar.gz" \
    && tar -xzf /tmp/mise.tgz -C /tmp \
    && mv /tmp/mise/bin/mise /usr/local/bin/mise \
    && chmod +x /usr/local/bin/mise \
    && rm -rf /tmp/mise /tmp/mise.tgz

# pnpm — required by frontend pre-PR checks. Pinned to the version the
# frontend monorepo declares in packageManager (pnpm@10.27.0).
ARG PNPM_VERSION=10.27.0
RUN npm install -g pnpm@${PNPM_VERSION}

# uv — Python package + project manager used by ai-api, bank-ingestor, and
# friends (their Makefiles invoke `uv sync` / `uv run pytest`). Static
# binary so no apt or pip bootstrap is needed.
ARG UV_VERSION=0.11.15
RUN ARCH=$(uname -m) \
    && curl -fsSL -o /tmp/uv.tgz "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-${ARCH}-unknown-linux-gnu.tar.gz" \
    && tar -xzf /tmp/uv.tgz -C /tmp \
    && mv /tmp/uv-${ARCH}-unknown-linux-gnu/uv /tmp/uv-${ARCH}-unknown-linux-gnu/uvx /usr/local/bin/ \
    && chmod +x /usr/local/bin/uv /usr/local/bin/uvx \
    && rm -rf /tmp/uv.tgz /tmp/uv-${ARCH}-unknown-linux-gnu

RUN ARCH=$(dpkg --print-architecture) \
    && TARBALL="go${GO_VERSION}.linux-${ARCH}.tar.gz" \
    && curl -fsSL -o "/tmp/${TARBALL}" "https://dl.google.com/go/${TARBALL}" \
    && curl -fsSL -o "/tmp/${TARBALL}.sha256" "https://dl.google.com/go/${TARBALL}.sha256" \
    && echo "$(cat /tmp/${TARBALL}.sha256)  /tmp/${TARBALL}" | sha256sum -c - \
    && tar -C /usr/local -xzf "/tmp/${TARBALL}" \
    && rm "/tmp/${TARBALL}" "/tmp/${TARBALL}.sha256"

ENV PATH="/usr/local/go/bin:${PATH}"

ARG CODEX_VERSION=0.132.0
RUN npm install -g @openai/codex@${CODEX_VERSION}

COPY codex/kelos_entrypoint.sh /kelos_entrypoint.sh
RUN chmod +x /kelos_entrypoint.sh

# Helper scripts for tasks that need git push + cluster read access. GitHub
# tokens are brokered by cody-tools; the task pod does not receive the
# GitHub App private key. See codex/scripts/README for the env contract.
COPY codex/scripts/cody-github-credential-helper /usr/local/bin/cody-github-credential-helper
COPY codex/scripts/cody-github-token /usr/local/bin/cody-github-token
COPY codex/scripts/github-app-credential-helper /usr/local/bin/github-app-credential-helper
COPY codex/scripts/github-app-token /usr/local/bin/github-app-token
COPY codex/scripts/kelos-agent-setup /usr/local/bin/kelos-agent-setup
# gh + npm + pnpm wrappers sit at /usr/local/bin (ahead of /usr/bin in
# PATH) so every `gh` invocation picks up an App installation token and
# every `npm`/`pnpm` invocation gets NODE_AUTH_TOKEN for the
# @quantum-wealth scope. pnpm needs its own wrapper because it does not
# shell out to npm — it reads .npmrc and the env directly.
COPY codex/scripts/gh /usr/local/bin/gh
COPY codex/scripts/npm /usr/local/bin/npm
COPY codex/scripts/pnpm /usr/local/bin/pnpm
RUN chmod +x /usr/local/bin/cody-github-credential-helper \
    /usr/local/bin/cody-github-token \
    /usr/local/bin/github-app-credential-helper \
    /usr/local/bin/github-app-token \
    /usr/local/bin/kelos-agent-setup \
    /usr/local/bin/gh \
    /usr/local/bin/npm \
    /usr/local/bin/pnpm

# Alpheya engineering skills — baked at image build time into the
# system-wide codex skills directory. Per https://developers.openai.com/codex/skills,
# Codex auto-discovers skills from /etc/codex/skills/<name>/SKILL.md at
# startup (no flag, no env var, no entrypoint logic required). The
# skill's `description` frontmatter tells codex when to trigger it.
#
# Source repo (quantum-wealth/skills) is private, so the build needs a
# GitHub token via BuildKit secret:
#   docker buildx build --secret id=github_token,env=GITHUB_TOKEN ...
# When the secret is absent the build still succeeds (with an empty
# /etc/codex/skills) so a developer without the token can iterate
# locally; codex just has no Alpheya skills to discover.
ARG ALPHEYA_SKILLS_REPO=quantum-wealth/skills
ARG ALPHEYA_SKILLS_REF=main
RUN --mount=type=secret,id=github_token \
    mkdir -p /etc/codex/skills \
    && if [ -s /run/secrets/github_token ]; then \
      TOKEN=$(cat /run/secrets/github_token) \
      && git clone --depth 1 --branch "${ALPHEYA_SKILLS_REF}" \
           "https://x-access-token:${TOKEN}@github.com/${ALPHEYA_SKILLS_REPO}.git" \
           /tmp/alpheya-skills-clone \
      && for skill_dir in /tmp/alpheya-skills-clone/plugins/alpheya-standards/skills/*/; do \
           [ -d "$skill_dir" ] || continue ; \
           cp -r "$skill_dir" /etc/codex/skills/ ; \
         done \
      && rm -rf /tmp/alpheya-skills-clone ; \
    else \
      echo "WARNING: github_token build secret not provided; /etc/codex/skills will be empty" >&2 ; \
    fi

ARG TARGETARCH
COPY bin/kelos-capture-linux-${TARGETARCH} /kelos/kelos-capture
COPY bin/kelos-session-runner-linux-${TARGETARCH} /kelos-session-runner
RUN chmod +x /kelos-session-runner

# JWT signing helpers — port of ai-agent/assay TokenSigningProvider.
# kelos-jwt: explicit CLI for shell scripts and debug commands.
# kelos-curl: transparent curl wrapper installed AS /usr/local/bin/curl
# so it shadows /usr/bin/curl on PATH; for hosts listed in
# ALPHEYA_TOKEN_SIGNING_HOSTS it auto-injects `Authorization: Bearer
# <jwt>` and exec's real curl. Config flows through TaskSpawner
# podOverrides.env (see codex/scripts/README.md).
COPY bin/kelos-jwt-linux-${TARGETARCH} /usr/local/bin/kelos-jwt
COPY bin/kelos-curl-linux-${TARGETARCH} /usr/local/bin/curl
RUN chmod +x /usr/local/bin/kelos-jwt /usr/local/bin/curl

# Move the real curl out of PATH and point the wrapper at its new home.
# Without this, an agent (or stray script) typing `/usr/bin/curl` could
# bypass JWT auth — the wrapper would not see the call at all. With the
# binary at /usr/local/libexec/curl-real, `which curl` only ever resolves
# to /usr/local/bin/curl (the kelos-curl wrapper); the wrapper itself
# reaches the real binary via KELOS_CURL_REAL.
#
# Done late so the earlier RUN ... apt-get / curl ... layers see the
# normal /usr/bin/curl. Build-time tooling is unaffected; only the
# runtime PATH changes.
RUN mkdir -p /usr/local/libexec \
    && mv /usr/bin/curl /usr/local/libexec/curl-real
ENV KELOS_CURL_REAL=/usr/local/libexec/curl-real

RUN useradd -u 61100 -m -s /bin/bash agent
RUN mkdir -p /home/agent/.codex /home/agent/.local/bin && chown -R agent:agent /home/agent

USER agent

ENV GOPATH="/home/agent/go"
ENV PATH="/home/agent/.local/bin:${GOPATH}/bin:${PATH}"

WORKDIR /workspace

ENTRYPOINT ["codex"]
