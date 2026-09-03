# GitHub environment setup tools

These scripts populate the GitHub Environment variables consumed by Chat's
deployment workflows. They create the GitHub Environment if it does not exist
and use outputs from the corresponding applied Terraform root.

Prerequisites:

- Authenticate the GitHub CLI with permission to administer Actions variables.
- Initialize and apply the relevant Terraform root first.
- Run from a checkout whose GitHub remote is `vsayfb/yevmiye-chat`, or set
  `GH_REPO=owner/repository` explicitly.

Configure staging from AWS Terraform:

```bash
./tools/configure-gh-staging.sh
```

The staging AWS region and Core/Chat instance tag default to `eu-central-1`
and `yevmiye-core-chat`. Override them with `STAGING_AWS_REGION` and
`STAGING_INSTANCE_NAME_TAG` when necessary.

Configure production from GCP Terraform:

```bash
./tools/configure-gh-production.sh
```

Pass an alternative Terraform root as the first argument to either script.
Both workflows use short-lived OIDC identities, so they require no static cloud
credential secrets in GitHub.
