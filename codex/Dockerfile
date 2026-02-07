FROM ubuntu:24.04

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

RUN npm install -g @openai/codex

RUN useradd -u 1200 -m -s /bin/bash codex
RUN mkdir -p /home/codex/.codex && chown -R codex:codex /home/codex

USER codex
WORKDIR /workspace

ENTRYPOINT ["codex"]
