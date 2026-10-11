output "cluster_name" {
  value = google_container_cluster.kiln.name
}

output "zone" {
  value = var.zone
}

output "get_credentials" {
  value = "gcloud container clusters get-credentials ${google_container_cluster.kiln.name} --zone ${var.zone} --project ${var.project_id}"
}

output "registry" {
  description = "Image prefix for `docker push`."
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.images.repository_id}"
}
