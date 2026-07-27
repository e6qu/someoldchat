# Exact versions, not ranges, and the same AWS provider version as
# deploy/ecs-scale-zero so a root module can consume both modules at once.
# Terraform resolves one provider version per configuration; the previous
# ">= 5.0, < 6.0" and ">= 5.0, < 7.0" pair made that impossible.
terraform {
  required_version = "1.13.5"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.55.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "3.9.0"
    }
  }
}
