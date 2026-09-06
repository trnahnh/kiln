package com.github.trnahnh.kiln.audit.requests;

import org.springframework.stereotype.Component;

import io.fabric8.kubernetes.api.model.GenericKubernetesResource;
import io.fabric8.kubernetes.client.KubernetesClient;
import io.fabric8.kubernetes.client.KubernetesClientException;
import io.fabric8.kubernetes.client.dsl.base.ResourceDefinitionContext;

@Component
public class Fabric8Applier implements ManifestApplier {

    static final String FIELD_MANAGER = "kiln-audit-service";

    private final KubernetesClient client;

    public Fabric8Applier(KubernetesClient client) {
        this.client = client;
    }

    @Override
    public GenericKubernetesResource apply(ResourceDefinitionContext context, GenericKubernetesResource manifest) {
        try {
            return client.genericKubernetesResources(context)
                    .inNamespace(manifest.getMetadata().getNamespace())
                    .resource(manifest)
                    .fieldManager(FIELD_MANAGER)
                    .forceConflicts()
                    .serverSideApply();
        } catch (KubernetesClientException e) {
            if (isAdmissionRejection(e)) {
                throw new AdmissionRejectedException(e.getCode(), e.getMessage(), e);
            }
            throw e;
        }
    }

    static boolean isAdmissionRejection(KubernetesClientException e) {
        int code = e.getCode();
        String message = e.getMessage() == null ? "" : e.getMessage();
        return code == 403 || code == 422 || (code >= 400 && code < 500 && message.contains("POLICY_DENIED"));
    }
}
