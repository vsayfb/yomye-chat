#!/usr/bin/env bash
set -euo pipefail

ENVIRONMENT="production"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_TERRAFORM_DIR="${SCRIPT_DIR}/../../infrastructure/gcp-infrastructure"
TERRAFORM_DIR="${1:-${DEFAULT_TERRAFORM_DIR}}"

for command_name in gh terraform; do
    if ! command -v "$command_name" >/dev/null 2>&1; then
        echo "$command_name is required." >&2
        exit 1
    fi
done

if [[ ! -d "$TERRAFORM_DIR" ]]; then
    echo "Terraform directory does not exist: $TERRAFORM_DIR" >&2
    exit 1
fi

gh auth status >/dev/null
REPOSITORY="${GH_REPO:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)}"

terraform_output() {
    terraform -chdir="$TERRAFORM_DIR" output -raw "$1"
}

set_variable() {
    local name="$1"
    local value="$2"

    if [[ -z "$value" ]]; then
        echo "Refusing to set an empty value for $name." >&2
        exit 1
    fi

    gh variable set "$name" \
        --repo "$REPOSITORY" \
        --env "$ENVIRONMENT" \
        --body "$value"
    echo "Set $ENVIRONMENT variable $name."
}

echo "Configuring GitHub environment $REPOSITORY:$ENVIRONMENT from $TERRAFORM_DIR"
gh api \
    --method PUT \
    "repos/${REPOSITORY}/environments/$ENVIRONMENT" >/dev/null

set_variable GCP_WORKLOAD_IDENTITY_PROVIDER "$(terraform_output github_workload_identity_provider)"
set_variable GCP_DEPLOY_SERVICE_ACCOUNT "$(terraform_output github_deploy_service_account)"
set_variable GCP_PROJECT_ID "$(terraform_output project_id)"
set_variable GCP_ZONE "$(terraform_output compute_zone)"
set_variable GCP_CORE_CHAT_INSTANCE "$(terraform_output core_chat_instance)"
set_variable GCS_DEPLOY_BUCKET "$(terraform_output app_deployments_bucket)"

echo "GitHub production environment configuration is complete."
