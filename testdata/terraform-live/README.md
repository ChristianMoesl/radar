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
acknowledge → Done while preserving the failed Terraform outcome.

The lifecycle harness then queues two non-speculative runs, verifies manual
confirmation requires Attention, cancels one run, and applies the other while
the isolated Radar daemon is stopped. Restart must recover Done with the same
task ID. Injected read failures verify retention and recovery against live
data without disrupting HCP Terraform. This stage updates only built-in fixture
state and requires zero planned resource destructions before apply. Cleanup
cancels/discards only runs created by that check; no destroy is issued.
