#!/usr/bin/env bash
set -euo pipefail

ENVIRONMENT="staging"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_TERRAFORM_DIR="${SCRIPT_DIR}/../../infrastructure/aws-infrastructure/environment/prod"
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
AWS_REGION_VALUE="${STAGING_AWS_REGION:-eu-central-1}"
INSTANCE_NAME_TAG_VALUE="${STAGING_INSTANCE_NAME_TAG:-yevmiye-core-chat}"

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

set_variable DEPLOY_ROLE_ARN "$(terraform_output github_actions_deploy_role_arn)"
set_variable AWS_REGION "$AWS_REGION_VALUE"
set_variable DEPLOY_BUCKET "$(terraform_output app_deployments_bucket_name)"
set_variable INSTANCE_NAME_TAG "$INSTANCE_NAME_TAG_VALUE"

echo "GitHub staging environment configuration is complete."
