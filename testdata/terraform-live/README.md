# Terraform live fixture

This fixture targets HCP Terraform organization `radar-oss`, workspace
`radar-oauth-e2e`. It uses only the built-in `terraform_data` resource and creates
no external infrastructure. The live workflow uses the trusted dispatch commit's
copy after environment approval; it needs only the existing `TFCTL_TOKEN` secret.

The normal CI job initializes with `-backend=false` and runs formatting,
validation and all three native plan-only tests without account credentials.
`fail_plan=true` deliberately fails a plan; changing `fixture_revision` exercises
a different plan input. The live workflow defaults to a successful speculative
plan and never applies or destroys state.
