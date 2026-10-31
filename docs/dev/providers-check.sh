#!/bin/sh
# Run inside an Iwato sandbox's terminal (a terminal opened after the
# provider was attached, or after restarting the sandbox) to check what
# the providers give it. Never prints a token: only whether a variable
# holds an OpenShell placeholder.
#
#   sh providers-check.sh github            # (a) gh api user
#   sh providers-check.sh git OWNER/REPO    # (b) clone a private repository (push: see providers.md)
#   sh providers-check.sh claude            # (c) claude -p with the iwato-claude-subscription provider
#   sh providers-check.sh atlassian         # (d) the Atlassian MCP through claude (iwato-atlassian)
#   sh providers-check.sh openrouter        # (e) list models and one completion
#   sh providers-check.sh huggingface       # (f) hf auth whoami, a small download
#   sh providers-check.sh civitai           # (g) /api/v1/me and a model listing
#   sh providers-check.sh vastai            # (h) the user and an offer search
#   sh providers-check.sh comfy             # (i) /api/user, v1 and v2 auth
#   sh providers-check.sh codex             # (j) codex exec with the iwato-codex-subscription provider
set -u
placeholder() {
  eval "v=\${$1:-}"
  case $v in
    "openshell:""resolve:env":*) echo "OK   $1 is an OpenShell placeholder" ;;
    '') echo "NG   $1 is not set (provider not attached, or this terminal was opened before attaching)"; return 1 ;;
    *) echo "NG   $1 is set but is not a placeholder"; return 1 ;;
  esac
}
case ${1:-} in
  github)
    placeholder GH_TOKEN; placeholder GITHUB_TOKEN
    echo "---- gh api user"
    gh api user --jq '"login: " + .login'
    ;;
  git)
    repo=${2:?usage: providers-check.sh git OWNER/REPO}
    placeholder GITHUB_TOKEN
    echo "---- git credential (the helper in /etc/gitconfig answers with the placeholder)"
    printf 'protocol=https\nhost=github.com\n\n' | git credential fill | sed -n 's/^username=/username=/p; s/^password=openshell:''resolve:env:.*/password=<placeholder>/p'
    dir=$(mktemp -d)
    echo "---- git clone https://github.com/$repo.git"
    git clone --depth 1 "https://github.com/$repo.git" "$dir/repo" && git -C "$dir/repo" log -1 --oneline
    echo "(clone kept in $dir/repo for a push test)"
    ;;
  claude)
    placeholder CLAUDE_CODE_OAUTH_TOKEN
    for v in ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN; do
      eval "x=\${$v:-}"; [ -n "$x" ] && echo "NOTE $v is set and takes precedence over CLAUDE_CODE_OAUTH_TOKEN"
    done
    echo "---- claude -p"
    claude -p 'Reply with exactly: PROVIDER-OK'
    ;;
  atlassian)
    placeholder ATLASSIAN_ACCESS_TOKEN
    echo "---- claude mcp get atlassian (the header must show the literal \${ATLASSIAN_ACCESS_TOKEN})"
    claude mcp get atlassian 2>&1 | sed 's/openshell:''resolve:env:[^ "]*/<placeholder: registered with double quotes, re-add with single quotes>/'
    echo "---- claude -p with the atlassian MCP"
    claude -p --allowedTools 'mcp__atlassian' 'Using only the tools of the atlassian MCP server, find the signed-in Atlassian user. Reply with exactly: ATLASSIAN-OK <their display name>. If the server is not connected or a tool fails, reply ATLASSIAN-NG and the error.'
    ;;
  openrouter)
    placeholder OPENROUTER_API_KEY
    echo "---- GET /api/v1/key (the key's own limits: 200 when the key reached OpenRouter)"
    curl -sS -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $OPENROUTER_API_KEY" https://openrouter.ai/api/v1/key
    echo "---- POST /api/v1/chat/completions"
    curl -sS https://openrouter.ai/api/v1/chat/completions -H "Authorization: Bearer $OPENROUTER_API_KEY" -H 'Content-Type: application/json' \
      -d '{"model":"openrouter/auto","max_tokens":16,"messages":[{"role":"user","content":"Reply with exactly: PROVIDER-OK"}]}' | head -c 600; echo
    ;;
  huggingface)
    placeholder HF_TOKEN; placeholder HUGGING_FACE_HUB_TOKEN
    echo "---- GET /api/whoami-v2 with curl (prints the user name)"
    curl -sS -H "Authorization: Bearer $HF_TOKEN" https://huggingface.co/api/whoami-v2 | sed -n 's/.*"name":"\([^"]*\)".*/name: \1/p'
    if command -v hf >/dev/null; then echo "---- hf auth whoami"; hf auth whoami
    elif command -v huggingface-cli >/dev/null; then echo "---- huggingface-cli whoami"; huggingface-cli whoami
    else echo "NOTE hf is not installed (uv tool install huggingface_hub, or pip install --user huggingface_hub)"; fi
    echo "---- a small file through /resolve/ (follows the CDN redirect, which gets no token)"
    curl -sSL -o /dev/null -w '%{http_code} %{url_effective}\n' -H "Authorization: Bearer $HF_TOKEN" https://huggingface.co/openai-community/gpt2/resolve/main/config.json | sed 's/?.*//'
    ;;
  civitai)
    placeholder CIVITAI_API_TOKEN; placeholder CIVITAI_TOKEN
    echo "---- GET /api/v1/me (bearer header)"
    curl -sS -H "Authorization: Bearer $CIVITAI_API_TOKEN" https://civitai.com/api/v1/me | head -c 300; echo
    echo "---- GET /api/v1/models?limit=1&favorites=true (?token= query)"
    curl -sS "https://civitai.com/api/v1/models?limit=1&favorites=true&token=$CIVITAI_TOKEN" | head -c 300; echo
    ;;
  vastai)
    placeholder VAST_API_KEY
    if command -v vastai >/dev/null; then echo "---- vastai show user"; vastai show user --raw | head -c 300; echo
    else echo "---- GET /api/v0/users/current/"; curl -sS -H "Authorization: Bearer $VAST_API_KEY" https://console.vast.ai/api/v0/users/current/ | head -c 300; echo; fi
    if command -v vastai >/dev/null; then echo "---- vastai search offers (PUT /api/v0/search/asks/)"; vastai search offers 'num_gpus=1' --limit 3; fi
    ;;
  comfy)
    placeholder COMFY_CLOUD_API_KEY; placeholder COMFY_API_KEY
    echo "---- GET /api/user (X-API-Key, v1)"
    curl -sS -o /dev/null -w '%{http_code}\n' -H "X-API-Key: $COMFY_CLOUD_API_KEY" https://cloud.comfy.org/api/user
    echo "---- GET /api/queue (X-API-Key, v1)"
    curl -sS -H "X-API-Key: $COMFY_CLOUD_API_KEY" https://cloud.comfy.org/api/queue | head -c 300; echo
    ;;
  codex)
    placeholder CODEX_CHATGPT_ACCESS_TOKEN; placeholder CODEX_CHATGPT_ACCOUNT_ID
    f=${CODEX_HOME:-$HOME/.codex}/auth.json
    if grep -qs '"refresh_token":"iwato-gateway-refreshes"' "$f"; then echo "OK   $f is Iwato's"; else echo "NG   $f is not Iwato's (codex logout, then a new terminal)"; fi
    echo "---- codex exec"
    codex exec --skip-git-repo-check 'Reply with exactly: PROVIDER-OK'
    ;;
  *) sed -n '2,16p' "$0"; exit 2 ;;
esac
