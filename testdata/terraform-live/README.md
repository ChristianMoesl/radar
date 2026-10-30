# Terraform live fixture

This fixture targets HCP Terraform organization `radar-oss`, workspace
`radar-oauth-e2e`. It uses only the built-in `terraform_data` resource and creates
no external infrastructure. The live workflow uses the trusted dispatch commit's
copy after environment approval; it needs only the existing `TFCTL_TOKEN` secret.

The normal CI job initializes with `-backend=false` and runs formatting,
validation and all three native plan-only tests without account credentials.
`fail_plan=true` deliberately fails a plan; changing `fixture_revision` exercises
a different plan input. Under one environment approval, the live workflow
qualifies a successful speculative plan (or the selected existing run), then
creates a deliberate failed speculative plan and requires Attention →
acknowledge → Done while preserving the failed Terraform outcome. It never
applies or destroys state.
