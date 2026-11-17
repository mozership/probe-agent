#!/bin/sh
# Keeps the OIDC token of the job for the Claude API in a file, which the
# Anthropic SDK in probe-agent exchanges for an access token through
# workload identity federation, as ANTHROPIC_IDENTITY_TOKEN_FILE names it.
#
# A token of GitHub Actions lasts 5 minutes, and each step of a workflow
# starts probe-agent anew, which exchanges the file again, so the file is
# written again every 2 minutes in the background until the job ends.
#
# Without the permission id-token: write, as in a pull request from a fork,
# there is no token: the script says so in the summary and sets nothing, and
# the steps that call the Claude API are skipped.
set -eu

if [ -z "${ACTIONS_ID_TOKEN_REQUEST_URL:-}" ]; then
  echo "No OIDC token for the Claude API; the e2e workflow is skipped." >> "$GITHUB_STEP_SUMMARY"
  exit 0
fi

file="$RUNNER_TEMP/anthropic-identity-token"

fetch() {
  curl -sSf -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
    "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=https://api.anthropic.com" |
    jq -r .value > "$file.new"
  mv "$file.new" "$file"
}

fetch
(while sleep 120; do fetch || true; done) > /dev/null 2>&1 &
echo "ANTHROPIC_IDENTITY_TOKEN_FILE=$file" >> "$GITHUB_ENV"
