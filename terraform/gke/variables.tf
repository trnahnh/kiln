variable "project_id" {
  description = "GCP project the cluster is created in. Billing must be enabled on it."
  type        = string
}

variable "region" {
  type    = string
  default = "us-central1"
}

# A zonal cluster: one control plane, and the GKE free tier waives the management fee for
# one zonal or Autopilot cluster per billing account.
variable "zone" {
  type    = string
  default = "us-central1-a"
}

variable "cluster_name" {
  type    = string
  default = "kiln-r2"
}

variable "machine_type" {
  description = "Node machine type for both pools; e2-standard-2 is 2 vCPU, 8 GB."
  type        = string
  default     = "e2-standard-2"
}

variable "on_demand_nodes" {
  type    = number
  default = 2
}

variable "spot_nodes" {
  type    = number
  default = 2
}

# The node economics labels the scheduler plugin reads (API_REFERENCE.md). The on-demand
# price is the e2-standard-2 list price in us-central1 at the time of writing; the spot
# price moves, so check `gcloud compute machine-types describe` or the pricing page before
# a run and pass the current value.
variable "on_demand_hourly_cost" {
  type    = string
  default = "0.067"
}

variable "spot_hourly_cost" {
  type    = string
  default = "0.020"
}

variable "spot_preemption_risk" {
  type    = string
  default = "0.2"
}

# Every resource carries this label so a stray one can be found and removed by it.
variable "owner_label" {
  type    = string
  default = "kiln-round2"
}
