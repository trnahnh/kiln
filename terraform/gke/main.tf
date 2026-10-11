# A minimal GKE Standard cluster for a short measurement window: one zonal control plane,
# an on-demand pool and a Spot pool carrying the node economics labels the kiln scheduler
# reads, and an Artifact Registry repository for the two images under test. Autopilot is
# not used because it does not run a second scheduler. Everything here is destroyed by
# `terraform destroy`; deletion protection is off on purpose.

locals {
  labels = {
    owner = var.owner_label
  }
  economics_label_prefix = "kiln.platform.internal/"
}

resource "google_artifact_registry_repository" "images" {
  location      = var.region
  repository_id = var.cluster_name
  format        = "DOCKER"
  labels        = local.labels
}

resource "google_container_cluster" "kiln" {
  name     = var.cluster_name
  location = var.zone

  # The pools below are the only nodes; the default pool is created and removed by GKE.
  remove_default_node_pool = true
  initial_node_count       = 1
  deletion_protection      = false

  release_channel {
    channel = "REGULAR"
  }

  # Nothing here needs cloud logging or monitoring; both bill per GiB ingested.
  logging_service    = "none"
  monitoring_service = "none"

  resource_labels = local.labels
}

resource "google_container_node_pool" "on_demand" {
  name       = "on-demand"
  cluster    = google_container_cluster.kiln.id
  node_count = var.on_demand_nodes

  node_config {
    machine_type = var.machine_type
    disk_size_gb = 50
    labels = {
      "${local.economics_label_prefix}capacity-type"   = "on-demand"
      "${local.economics_label_prefix}hourly-cost"     = var.on_demand_hourly_cost
      "${local.economics_label_prefix}preemption-risk" = "0"
    }
    resource_labels = local.labels
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]
  }
}

resource "google_container_node_pool" "spot" {
  name       = "spot"
  cluster    = google_container_cluster.kiln.id
  node_count = var.spot_nodes

  node_config {
    machine_type = var.machine_type
    disk_size_gb = 50
    spot         = true
    labels = {
      "${local.economics_label_prefix}capacity-type"   = "spot"
      "${local.economics_label_prefix}hourly-cost"     = var.spot_hourly_cost
      "${local.economics_label_prefix}preemption-risk" = var.spot_preemption_risk
    }
    resource_labels = local.labels
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]
  }
}
