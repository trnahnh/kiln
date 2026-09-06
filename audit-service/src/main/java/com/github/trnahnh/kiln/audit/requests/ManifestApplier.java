package com.github.trnahnh.kiln.audit.requests;

import io.fabric8.kubernetes.client.dsl.base.ResourceDefinitionContext;
import io.fabric8.kubernetes.api.model.GenericKubernetesResource;

/** Applies a manifest to the cluster; the Fabric8 implementation is swapped out in tests. */
public interface ManifestApplier {

    /** Returns the applied object; throws AdmissionRejectedException when admission refuses it. */
    GenericKubernetesResource apply(ResourceDefinitionContext context, GenericKubernetesResource manifest);
}
