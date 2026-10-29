run "default_plan" {
  command = plan

  assert {
    condition     = terraform_data.fixture.input.fixture == "radar-oauth-e2e" && terraform_data.fixture.input.revision == "initial"
    error_message = "The default plan must identify the disposable Radar fixture."
  }
}

run "changed_revision" {
  command = plan

  variables {
    fixture_revision = "next"
  }

  assert {
    condition     = terraform_data.fixture.input.revision == "next"
    error_message = "The revision input must be reflected in the plan."
  }
}

run "deliberate_failure" {
  command = plan

  variables {
    fail_plan = true
  }

  expect_failures = [terraform_data.fixture]
}
