mock_provider "aws" {}

variables {
  deployment_id       = "deadbeef1234"
  enable_environment  = true
  media_image_digest  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
  runner_image_digest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
  apply_started_at    = "2026-09-08T03:39:31.4568511+00:00"
  expires_at          = "2026-09-08T05:39:31.4568511+00:00"
  expected_cost_usd   = 3
}

run "exact_two_hours_with_fractional_seconds" {
  command = plan
}

run "reject_fraction_beyond_two_hours" {
  command = plan
  variables {
    expires_at = "2026-09-08T05:39:31.4568512+00:00"
  }
  expect_failures = [terraform_data.deadline_guard]
}

run "reject_zero_duration" {
  command = plan
  variables {
    expires_at = "2026-09-08T03:39:31.4568511+00:00"
  }
  expect_failures = [terraform_data.deadline_guard]
}
