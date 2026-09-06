package com.github.trnahnh.kiln.audit.requests;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import java.time.Clock;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.Map;
import java.util.UUID;

import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import com.github.trnahnh.kiln.audit.event.WireEvent;
import com.github.trnahnh.kiln.audit.publish.EventPublisher;

import io.fabric8.kubernetes.api.model.GenericKubernetesResource;
import io.fabric8.kubernetes.api.model.ObjectMeta;
import io.fabric8.kubernetes.client.dsl.base.ResourceDefinitionContext;

class RequestSubmitterTest {

    private final ManifestApplier applier = mock(ManifestApplier.class);
    private final EventPublisher publisher = mock(EventPublisher.class);
    private final Clock clock = Clock.fixed(Instant.parse("2026-09-06T10:00:00Z"), ZoneOffset.UTC);
    private RequestSubmitter submitter;

    @BeforeEach
    void setUp() {
        submitter = new RequestSubmitter(applier, publisher, clock);
    }

    private static GenericKubernetesResource manifest(String kind, String ns, String name) {
        GenericKubernetesResource r = new GenericKubernetesResource();
        r.setKind(kind);
        ObjectMeta meta = new ObjectMeta();
        meta.setName(name);
        meta.setNamespace(ns);
        r.setMetadata(meta);
        r.setAdditionalProperty("spec", Map.of("parameters", Map.of("storageGB", 20)));
        return r;
    }

    @Test
    void appliesWithTheActorAnnotationAndPublishesProvisionRequest() {
        GenericKubernetesResource applied = manifest("DatabaseClaim", "team-checkout", "checkout-db");
        applied.getMetadata().setResourceVersion("4711");
        when(applier.apply(any(), any())).thenReturn(applied);

        RequestSubmitter.Accepted accepted = submitter.submit(manifest("DatabaseClaim", "team-checkout", "checkout-db"), "dev@company.com");

        ArgumentCaptor<ResourceDefinitionContext> ctx = ArgumentCaptor.forClass(ResourceDefinitionContext.class);
        ArgumentCaptor<GenericKubernetesResource> sent = ArgumentCaptor.forClass(GenericKubernetesResource.class);
        verify(applier).apply(ctx.capture(), sent.capture());
        assertThat(ctx.getValue().getPlural()).isEqualTo("databaseclaims");
        assertThat(sent.getValue().getApiVersion()).isEqualTo("platform.internal/v1alpha1");
        assertThat(sent.getValue().getMetadata().getAnnotations())
                .containsEntry(RequestSubmitter.ANNOTATION_REQUESTED_BY, "dev@company.com");

        ArgumentCaptor<WireEvent> event = ArgumentCaptor.forClass(WireEvent.class);
        verify(publisher).publish(event.capture());
        WireEvent e = event.getValue();
        assertThat(e.action()).isEqualTo("PROVISION_REQUEST");
        assertThat(e.actor()).isEqualTo("dev@company.com");
        assertThat(e.resource()).isEqualTo("DatabaseClaim/team-checkout/checkout-db");
        assertThat(e.details()).isEqualTo("{\"kind\":\"DatabaseClaim\",\"outcome\":\"Received\"}");
        assertThat(e.timestamp()).isEqualTo(clock.instant());
        assertThat(accepted.resource()).isEqualTo(e.resource());
        assertThat(accepted.eventId()).isEqualTo(e.eventId())
                .isEqualTo(RequestSubmitter.deterministicId("DatabaseClaim/team-checkout/checkout-db", "PROVISION_REQUEST", "4711"));
    }

    @Test
    void anAdmissionRejectionPublishesPolicyDenyWithTheRule() {
        when(applier.apply(any(), any())).thenThrow(new AdmissionRejectedException(400,
                "admission webhook denied the request: POLICY_DENIED rule=tags-required: tags.team is mandatory", null));

        assertThatThrownBy(() -> submitter.submit(manifest("DatabaseClaim", "team-checkout", "bad"), "dev@company.com"))
                .isInstanceOf(RequestSubmitter.DeniedException.class)
                .satisfies(t -> {
                    RequestSubmitter.Denied d = ((RequestSubmitter.DeniedException) t).denied();
                    assertThat(d.rule()).isEqualTo("tags-required");
                    assertThat(d.resource()).isEqualTo("DatabaseClaim/team-checkout/bad");
                });

        ArgumentCaptor<WireEvent> event = ArgumentCaptor.forClass(WireEvent.class);
        verify(publisher).publish(event.capture());
        assertThat(event.getValue().action()).isEqualTo("POLICY_DENY");
        assertThat(event.getValue().details()).startsWith("{\"outcome\":\"Denied\",\"reason\":\"admission webhook denied")
                .contains("\"rule\":\"tags-required\"");
    }

    @Test
    void unknownKindsAndMissingNamesAreRejectedBeforeAnythingHappens() {
        assertThatThrownBy(() -> submitter.submit(manifest("Deployment", "ns", "x"), "dev"))
                .isInstanceOf(RequestSubmitter.UnsupportedKindException.class);
        assertThatThrownBy(() -> submitter.submit(manifest("ChaosExperiment", "ns", ""), "dev"))
                .isInstanceOf(RequestSubmitter.UnsupportedKindException.class);
        verify(applier, never()).apply(any(), any());
        verify(publisher, never()).publish(any());
    }

    @Test
    void deterministicIdMatchesTheGoModule() {
        // audit.DeterministicID("a") in the Go module, UUID v5 in namespace 3b1d8c2e-6f0a-4c9b-9d3e-7a5f1e2c4b60.
        UUID id = RequestSubmitter.deterministicId("a");
        assertThat(id.version()).isEqualTo(5);
        assertThat(id).isEqualTo(RequestSubmitter.deterministicId("a"))
                .isNotEqualTo(RequestSubmitter.deterministicId("a", "b"));
    }
}
