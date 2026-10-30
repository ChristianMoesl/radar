variable "fixture_revision" {
  description = "Non-secret marker that can be changed to exercise a planned update."
  type        = string
  default     = "initial"
}

variable "fail_plan" {
  description = "Deliberately reject planning to exercise terminal failure and acknowledgement."
  type        = bool
  default     = false
}
