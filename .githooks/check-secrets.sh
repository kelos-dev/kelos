#!/usr/bin/env bash
# Pre-commit hook: check staged files for secrets or sensitive information

set -euo pipefail

RED='\033[0;31m'
NC='\033[0m'

# Patterns that suggest secrets or sensitive info
PATTERNS=(
  'AKIA[0-9A-Z]{16}'                                      # AWS Access Key ID
  '["\x27]sk-[a-zA-Z0-9]{20,}'                            # OpenAI / Stripe secret keys
  'ghp_[a-zA-Z0-9]{36}'                                   # GitHub personal access token
  'github_pat_[a-zA-Z0-9_]{22,}'                          # GitHub fine-grained PAT
  'glpat-[a-zA-Z0-9\-]{20,}'                              # GitLab PAT
  'xox[bpors]-[a-zA-Z0-9\-]+'                             # Slack tokens
  '-----BEGIN (RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----'  # Private keys
  'password\s*[:=]\s*["\x27][^"\x27]{4,}'                 # password assignments
  'secret\s*[:=]\s*["\x27][^"\x27]{4,}'                   # secret assignments
  'api[_-]?key\s*[:=]\s*["\x27][^"\x27]{4,}'              # API key assignments
  'token\s*[:=]\s*["\x27][^"\x27]{4,}'                    # token assignments
  'AIza[0-9A-Za-z\-_]{35}'                                # Google API key
  '[0-9]+-[0-9A-Za-z_]{32}\.apps\.googleusercontent\.com' # Google OAuth client ID
  'sk-ant-api[0-9]{2}-[a-zA-Z0-9\-_]{80,}'                # Anthropic API key
  'sk-ant-[a-zA-Z0-9\-_]{40,}'                            # Anthropic API key (older format)
  'sk-proj-[a-zA-Z0-9\-_]{40,}'                           # OpenAI project API key
  'sk-[a-zA-Z0-9]{48}'                                    # OpenAI API key (legacy)
  'ya29\.[a-zA-Z0-9_\-]{50,}'                             # Google/Vertex OAuth access token
)

STAGED_FILES=$(git diff --cached --name-only --diff-filter=ACM 2>/dev/null || true)

if [ -z "$STAGED_FILES" ]; then
  exit 0
fi

FOUND=0

for file in $STAGED_FILES; do
  # Skip binary files and common non-secret files
  if [[ "$file" =~ \.(png|jpg|jpeg|gif|ico|woff|woff2|ttf|eot|pdf|zip|tar|gz)$ ]]; then
    continue
  fi
  # Skip this hook script itself and test fixtures
  if [[ "$file" == *"check-secrets"* ]] || [[ "$file" == *"testdata"* ]] || [[ "$file" == *"test/fixtures"* ]]; then
    continue
  fi

  CONTENT=$(git show ":$file" 2>/dev/null || true)
  if [ -z "$CONTENT" ]; then
    continue
  fi

  for pattern in "${PATTERNS[@]}"; do
    MATCHES=$(echo "$CONTENT" | grep -nEi "$pattern" 2>/dev/null || true)
    if [ -n "$MATCHES" ]; then
      echo -e "${RED}Possible secret found in ${file}:${NC}"
      echo "$MATCHES" | head -5
      echo ""
      FOUND=1
    fi
  done
done

# Check for common sensitive filenames
SENSITIVE_FILES=(
  '.env'
  '.env.local'
  '.env.production'
  'credentials.json'
  'service-account.json'
  'id_rsa'
  'id_ed25519'
  '.npmrc'
  '.pypirc'
  'kubeconfig'
)

for file in $STAGED_FILES; do
  basename=$(basename "$file")
  for sensitive in "${SENSITIVE_FILES[@]}"; do
    if [ "$basename" = "$sensitive" ]; then
      echo -e "${RED}Sensitive file staged for commit: ${file}${NC}"
      FOUND=1
    fi
  done
done

if [ "$FOUND" -eq 1 ]; then
  echo -e "${RED}Commit blocked: potential secrets detected.${NC}"
  echo "If these are false positives, commit with --no-verify to bypass."
  exit 1
fi

exit 0
