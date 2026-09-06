package com.github.trnahnh.kiln.audit.requests;

import java.nio.charset.StandardCharsets;
import java.time.Clock;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.UUID;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import org.springframework.stereotype.Service;

import com.github.trnahnh.kiln.audit.chain.CanonicalJson;
import com.github.trnahnh.kiln.audit.event.WireEvent;
import com.github.trnahnh.kiln.audit.publish.EventPublisher;

import io.fabric8.kubernetes.api.model.GenericKubernetesResource;
import io.fabric8.kubernetes.api.model.ObjectMeta;
import io.fabric8.kubernetes.client.dsl.base.ResourceDefinitionContext;

@Service
public class RequestSubmitter {

    public static final String ANNOTATION_REQUESTED_BY = "platform.internal/requested-by";
    public static final String ACTION_PROVISION_REQUEST = "PROVISION_REQUEST";
    public static final String ACTION_POLICY_DENY = "POLICY_DENY";

    private static final Pattern RULE = Pattern.compile("rule=([^\\s:]+)");

    /** Mirrors audit.DeterministicID in the Go module: UUID v5 over NUL-joined parts. */
    private static final UUID ID_NAMESPACE = UUID.fromString("3b1d8c2e-6f0a-4c9b-9d3e-7a5f1e2c4b60");

    static final Map<String, ResourceDefinitionContext> KINDS = Map.of(
            "DatabaseClaim", context("platform.internal", "v1alpha1", "DatabaseClaim", "databaseclaims"),
            "CanaryRollout", context("platform.internal", "v1", "CanaryRollout", "canaryrollouts"),
            "ChaosExperiment", context("platform.internal", "v1", "ChaosExperiment", "chaosexperiments"));

    private final ManifestApplier applier;
    private final EventPublisher publisher;
    private final Clock clock;

    public RequestSubmitter(ManifestApplier applier, EventPublisher publisher, Clock clock) {
        this.applier = applier;
        this.publisher = publisher;
        this.clock = clock;
    }

    public record Accepted(String resource, UUID eventId) {
    }

    public record Denied(String resource, UUID eventId, String rule, String reason) {
    }

    public static class UnsupportedKindException extends RuntimeException {
        public UnsupportedKindException(String message) {
            super(message);
        }
    }

    public static class DeniedException extends RuntimeException {
        private final Denied denied;

        public DeniedException(Denied denied, Throwable cause) {
            super(denied.reason(), cause);
            this.denied = denied;
        }

        public Denied denied() {
            return denied;
        }
    }

    public Accepted submit(GenericKubernetesResource manifest, String subject) {
        ResourceDefinitionContext context = KINDS.get(manifest.getKind());
        if (context == null) {
            throw new UnsupportedKindException("kind must be one of " + KINDS.keySet() + ", got " + manifest.getKind());
        }
        manifest.setApiVersion(context.getGroup() + "/" + context.getVersion());
        ObjectMeta meta = manifest.getMetadata() == null ? new ObjectMeta() : manifest.getMetadata();
        if (meta.getName() == null || meta.getName().isBlank()) {
            throw new UnsupportedKindException("metadata.name is required");
        }
        if (meta.getNamespace() == null || meta.getNamespace().isBlank()) {
            meta.setNamespace("default");
        }
        Map<String, String> annotations = meta.getAnnotations() == null ? new LinkedHashMap<>() : new LinkedHashMap<>(meta.getAnnotations());
        annotations.put(ANNOTATION_REQUESTED_BY, subject);
        meta.setAnnotations(annotations);
        manifest.setMetadata(meta);
        String resource = manifest.getKind() + "/" + meta.getNamespace() + "/" + meta.getName();

        try {
            GenericKubernetesResource applied = applier.apply(context, manifest);
            String version = applied.getMetadata() == null ? "" : firstNonBlank(applied.getMetadata().getResourceVersion(), applied.getMetadata().getUid());
            UUID eventId = deterministicId(resource, ACTION_PROVISION_REQUEST, version);
            publisher.publish(new WireEvent(eventId, subject, ACTION_PROVISION_REQUEST, resource, clock.instant(),
                    details(Map.of("outcome", "Received", "kind", manifest.getKind()))));
            return new Accepted(resource, eventId);
        } catch (AdmissionRejectedException e) {
            String reason = e.getMessage() == null ? "admission rejected the request" : e.getMessage();
            Matcher m = RULE.matcher(reason);
            String rule = m.find() ? m.group(1) : null;
            UUID eventId = deterministicId(resource, ACTION_POLICY_DENY, String.valueOf(clock.instant().toEpochMilli()));
            Map<String, Object> details = new LinkedHashMap<>();
            details.put("outcome", "Denied");
            if (rule != null) {
                details.put("rule", rule);
            }
            details.put("reason", reason);
            publisher.publish(new WireEvent(eventId, subject, ACTION_POLICY_DENY, resource, clock.instant(), details(details)));
            throw new DeniedException(new Denied(resource, eventId, rule, reason), e);
        }
    }

    static String details(Map<String, ?> values) {
        return CanonicalJson.canonicalize(CanonicalJson.mapper().valueToTree(values));
    }

    static UUID deterministicId(String... parts) {
        String joined = String.join("\0", parts);
        return uuidV5(ID_NAMESPACE, joined.getBytes(StandardCharsets.UTF_8));
    }

    private static UUID uuidV5(UUID namespace, byte[] name) {
        try {
            java.security.MessageDigest sha1 = java.security.MessageDigest.getInstance("SHA-1");
            sha1.update(toBytes(namespace));
            sha1.update(name);
            byte[] hash = sha1.digest();
            hash[6] = (byte) ((hash[6] & 0x0f) | 0x50);
            hash[8] = (byte) ((hash[8] & 0x3f) | 0x80);
            long msb = 0;
            long lsb = 0;
            for (int i = 0; i < 8; i++) {
                msb = (msb << 8) | (hash[i] & 0xff);
            }
            for (int i = 8; i < 16; i++) {
                lsb = (lsb << 8) | (hash[i] & 0xff);
            }
            return new UUID(msb, lsb);
        } catch (java.security.NoSuchAlgorithmException e) {
            throw new IllegalStateException("SHA-1 unavailable", e);
        }
    }

    private static byte[] toBytes(UUID uuid) {
        byte[] out = new byte[16];
        long msb = uuid.getMostSignificantBits();
        long lsb = uuid.getLeastSignificantBits();
        for (int i = 0; i < 8; i++) {
            out[i] = (byte) (msb >>> (8 * (7 - i)));
            out[8 + i] = (byte) (lsb >>> (8 * (7 - i)));
        }
        return out;
    }

    private static String firstNonBlank(String a, String b) {
        if (a != null && !a.isBlank()) {
            return a;
        }
        return b == null ? "" : b;
    }

    private static ResourceDefinitionContext context(String group, String version, String kind, String plural) {
        return new ResourceDefinitionContext.Builder()
                .withGroup(group).withVersion(version).withKind(kind).withPlural(plural).withNamespaced(true)
                .build();
    }
}
