output "fixture_revision" {
  description = "The non-secret revision selected for this fixture plan."
  value       = terraform_data.fixture.input.revision
}
