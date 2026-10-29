terraform {
  required_version = ">= 1.6.0, < 2.0.0"

  cloud {
    hostname     = "app.terraform.io"
    organization = "radar-oss"

    workspaces {
      name = "radar-oauth-e2e"
    }
  }
}

# The built-in provider stores data in Terraform state only. No external
# providers, cloud resources, local commands, or service credentials are used.
resource "terraform_data" "fixture" {
  input = {
    fixture  = "radar-oauth-e2e"
    revision = var.fixture_revision
  }

  lifecycle {
    precondition {
      condition     = !var.fail_plan
      error_message = "Intentional Radar E2E plan failure. Set fail_plan=false for a successful plan."
    }
  }
}
