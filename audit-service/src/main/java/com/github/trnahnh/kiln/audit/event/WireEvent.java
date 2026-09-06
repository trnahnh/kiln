package com.github.trnahnh.kiln.audit.event;

import java.time.Instant;
import java.util.UUID;

/** The wire shape of API_REFERENCE.md "Audit event schema"; details is canonical JSON text. */
public record WireEvent(UUID eventId, String actor, String action, String resource, Instant timestamp,
                        String details) {
}
