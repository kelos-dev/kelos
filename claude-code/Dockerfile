FROM ubuntu:24.04

ARG GO_VERSION=1.25.0

RUN apt-get update && apt-get install -y \
    build-essential \
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

RUN npm install -g @anthropic-ai/claude-code

RUN useradd -u 1100 -m -s /bin/bash claude
RUN mkdir -p /home/claude/.claude && chown -R claude:claude /home/claude

USER claude

ENV GOPATH="/home/claude/go"
ENV PATH="${GOPATH}/bin:${PATH}"

WORKDIR /workspace

ENTRYPOINT ["claude"]
