FROM mcr.microsoft.com/devcontainers/go:1.25-bookworm

USER root

SHELL ["/bin/bash", "-euo", "pipefail", "-c"]

# kind (for local K8s clusters)
RUN curl -Lo /usr/local/bin/kind https://kind.sigs.k8s.io/dl/latest/kind-linux-amd64 \
    && chmod +x /usr/local/bin/kind

# kubectx and kubens (for switching Kubernetes contexts and namespaces)
RUN curl -fsSL https://github.com/ahmetb/kubectx/releases/latest/download/kubectx -o /usr/local/bin/kubectx \
    && curl -fsSL https://github.com/ahmetb/kubectx/releases/latest/download/kubens -o /usr/local/bin/kubens \
    && chmod +x /usr/local/bin/kubectx /usr/local/bin/kubens

# Claude Code
RUN curl -fsSL https://claude.ai/install.sh | bash

# Install AWS CLI
RUN cd $(mktemp -d) && \
    curl "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o "awscliv2.zip" && \
    unzip awscliv2.zip && \
    ./aws/install && \
    cd / && \
    rm -rf "$OLDPWD"

# asdf (for teleport-ent)
# renovate: datasource=github-releases depName=asdf-vm/asdf
ENV V_ASDF="0.18.1"
RUN <<EOF
    curl -L https://github.com/asdf-vm/asdf/releases/download/v${V_ASDF}/asdf-v${V_ASDF}-linux-amd64.tar.gz -o /usr/local/bin/asdf.tar.gz
    tar -xzf /usr/local/bin/asdf.tar.gz -C /usr/local/bin
    rm /usr/local/bin/asdf.tar.gz
    echo "export ASDF_DATA_DIR='/root/.asdf'" >>/etc/zsh/zshrc
    echo "export PATH=\"\$ASDF_DATA_DIR/shims:\$PATH\"" >>/etc/zsh/zshrc
    echo "export ASDF_DATA_DIR='/root/.asdf'" >>/etc/bash.bashrc
    echo "export PATH=\"\$ASDF_DATA_DIR/shims:\$PATH\"" >>/etc/bash.bashrc
    asdf plugin add teleport-ent
    asdf install teleport-ent 18.2.2
    asdf set --home teleport-ent 18.2.2
EOF

# AWS IAM Roles Anywhere
RUN <<EOB
    mkdir -p /opt/roles-anywhere /opt/roles-anywhere-admin /opt/roles-anywhere-sandbox /opt/roles-anywhere-bd /opt/roles-anywhere-cs /opt/roles-anywhere-gtm
    chown -R root:root /opt/roles-anywhere
    curl -fsSL https://rolesanywhere.amazonaws.com/releases/1.3.0/X86_64/Linux/aws_signing_helper -o /usr/local/bin/aws_signing_helper && chmod +x /usr/local/bin/aws_signing_helper
    mkdir -p /root/.aws
    cat <<EOF > /root/.aws/config
[default]
credential_process = /usr/local/bin/aws_signing_helper credential-process --certificate /opt/roles-anywhere/svid.pem --private-key /opt/roles-anywhere/svid_key.pem --profile-arn arn:aws:rolesanywhere:us-west-1:580663733917:profile/1c5babda-a4a9-4c75-9a16-88e143ab0233 --trust-anchor-arn arn:aws:rolesanywhere:us-west-1:580663733917:trust-anchor/b15762fd-2a5f-4bcd-a09f-17f63f4f2f98 --role-arn arn:aws:iam::580663733917:role/TeleportDeveloperAccess
output = json
[profile teleport-admin]
credential_process = /usr/local/bin/aws_signing_helper credential-process --certificate /opt/roles-anywhere-admin/svid.pem --private-key /opt/roles-anywhere-admin/svid_key.pem --profile-arn arn:aws:rolesanywhere:us-west-1:580663733917:profile/32163bf3-dd34-4b50-8ff2-bea86d8daf3b --trust-anchor-arn arn:aws:rolesanywhere:us-west-1:580663733917:trust-anchor/b15762fd-2a5f-4bcd-a09f-17f63f4f2f98 --role-arn arn:aws:iam::580663733917:role/TeleportAdministratorAccess
output = json
[profile sandbox]
credential_process = /usr/local/bin/aws_signing_helper credential-process --certificate /opt/roles-anywhere-sandbox/svid.pem --private-key /opt/roles-anywhere-sandbox/svid_key.pem --profile-arn arn:aws:rolesanywhere:us-east-1:774922483191:profile/bf98f431-d465-48d8-84b1-6d3090bb17aa --trust-anchor-arn arn:aws:rolesanywhere:us-east-1:774922483191:trust-anchor/9f687ddc-5ae6-4459-bd1f-ed9c7d296f25 --role-arn arn:aws:iam::774922483191:role/TeleportRolesAnywhere
region = us-west-1
output = json
[profile bd]
credential_process = /usr/local/bin/aws_signing_helper credential-process --certificate /opt/roles-anywhere-bd/svid.pem --private-key /opt/roles-anywhere-bd/svid_key.pem --profile-arn arn:aws:rolesanywhere:us-west-1:580663733917:profile/e61115ef-b5d7-415f-b249-6fd1f3814aa1 --trust-anchor-arn arn:aws:rolesanywhere:us-west-1:580663733917:trust-anchor/b15762fd-2a5f-4bcd-a09f-17f63f4f2f98 --role-arn arn:aws:iam::580663733917:role/TeleportBusinessDevelopmentAccess
output = json
[profile cs]
credential_process = /usr/local/bin/aws_signing_helper credential-process --certificate /opt/roles-anywhere-cs/svid.pem --private-key /opt/roles-anywhere-cs/svid_key.pem --profile-arn arn:aws:rolesanywhere:us-west-1:580663733917:profile/8bbcfc3e-f5e2-4d17-a7c4-fb10f648b10e --trust-anchor-arn arn:aws:rolesanywhere:us-west-1:580663733917:trust-anchor/b15762fd-2a5f-4bcd-a09f-17f63f4f2f98 --role-arn arn:aws:iam::580663733917:role/TeleportCustomerSolutionsAccess
output = json
[profile gtm]
credential_process = /usr/local/bin/aws_signing_helper credential-process --certificate /opt/roles-anywhere-gtm/svid.pem --private-key /opt/roles-anywhere-gtm/svid_key.pem --profile-arn arn:aws:rolesanywhere:us-east-1:649126925216:profile/de329f63-770f-462c-9152-ea822877c9f3 --trust-anchor-arn arn:aws:rolesanywhere:us-east-1:649126925216:trust-anchor/a784c1f4-8247-4b9d-b565-d430fec69f15 --role-arn arn:aws:iam::649126925216:role/TeleportAdminAccess
region = us-west-1
output = json
EOF
EOB

# claude-bedrock helper and gimme-creds aliases
RUN <<'EOF'
cat >> /etc/zshrc << 'SHELL_RC'

# Claude / Bedrock helper function
claude-bedrock() {
    # Fetch credentials using aws_signing_helper
    local creds=$(/usr/local/bin/aws_signing_helper credential-process \
        --certificate /opt/roles-anywhere/svid.pem \
        --private-key /opt/roles-anywhere/svid_key.pem \
        --profile-arn arn:aws:rolesanywhere:us-west-1:580663733917:profile/1c5babda-a4a9-4c75-9a16-88e143ab0233 \
        --trust-anchor-arn arn:aws:rolesanywhere:us-west-1:580663733917:trust-anchor/b15762fd-2a5f-4bcd-a09f-17f63f4f2f98 \
        --role-arn arn:aws:iam::580663733917:role/TeleportDeveloperAccess 2>/dev/null)

    # Export credentials as environment variables
    export AWS_ACCESS_KEY_ID=$(echo "$creds" | jq -r '.AccessKeyId')
    export AWS_SECRET_ACCESS_KEY=$(echo "$creds" | jq -r '.SecretAccessKey')
    export AWS_SESSION_TOKEN=$(echo "$creds" | jq -r '.SessionToken')

    # Call claude with Bedrock enabled
    CLAUDE_CODE_USE_BEDROCK=1 AWS_REGION=us-west-2 claude "$@"
}

# Teleport credential helpers
alias gimme-creds="tsh svid issue --output /opt/roles-anywhere --svid-ttl 12h /svc/codespaces"
alias gimme-admin-creds="tsh svid issue --output /opt/roles-anywhere-admin --svid-ttl 12h /role/administrator"
alias gimme-sandbox-creds="tsh svid issue --output /opt/roles-anywhere-sandbox --svid-ttl 12h /cloud/aws-sandbox"
SHELL_RC
cat >> /etc/bash.bashrc << 'SHELL_RC'

# Claude / Bedrock helper function
claude-bedrock() {
    # Fetch credentials using aws_signing_helper
    local creds=$(/usr/local/bin/aws_signing_helper credential-process \
        --certificate /opt/roles-anywhere/svid.pem \
        --private-key /opt/roles-anywhere/svid_key.pem \
        --profile-arn arn:aws:rolesanywhere:us-west-1:580663733917:profile/1c5babda-a4a9-4c75-9a16-88e143ab0233 \
        --trust-anchor-arn arn:aws:rolesanywhere:us-west-1:580663733917:trust-anchor/b15762fd-2a5f-4bcd-a09f-17f63f4f2f98 \
        --role-arn arn:aws:iam::580663733917:role/TeleportDeveloperAccess 2>/dev/null)

    # Export credentials as environment variables
    export AWS_ACCESS_KEY_ID=$(echo "$creds" | jq -r '.AccessKeyId')
    export AWS_SECRET_ACCESS_KEY=$(echo "$creds" | jq -r '.SecretAccessKey')
    export AWS_SESSION_TOKEN=$(echo "$creds" | jq -r '.SessionToken')

    # Call claude with Bedrock enabled
    CLAUDE_CODE_USE_BEDROCK=1 AWS_REGION=us-west-2 claude "$@"
}

# Teleport credential helpers
alias gimme-creds="tsh svid issue --output /opt/roles-anywhere --svid-ttl 12h /svc/codespaces"
alias gimme-admin-creds="tsh svid issue --output /opt/roles-anywhere-admin --svid-ttl 12h /role/administrator"
alias gimme-sandbox-creds="tsh svid issue --output /opt/roles-anywhere-sandbox --svid-ttl 12h /cloud/aws-sandbox"
SHELL_RC
EOF

# Needed for teleport & credentials
RUN <<EOF
    echo 'export TELEPORT_AUTH=google' >>/etc/zshrc
    echo 'export TELEPORT_PROXY=anomalo.teleport.sh:443' >>/etc/zshrc
    echo 'export TELEPORT_AUTH=google' >>/etc/bash.bashrc
    echo 'export TELEPORT_PROXY=anomalo.teleport.sh:443' >>/etc/bash.bashrc
EOF
