package com.github.trnahnh.kiln.audit.store;

import java.time.Instant;
import java.util.UUID;

import org.hibernate.annotations.JdbcTypeCode;
import org.hibernate.type.SqlTypes;

import com.github.trnahnh.kiln.audit.chain.HashChain;

import jakarta.persistence.Column;
import jakarta.persistence.Entity;
import jakarta.persistence.GeneratedValue;
import jakarta.persistence.GenerationType;
import jakarta.persistence.Id;
import jakarta.persistence.Table;

@Entity
@Table(name = "audit_entry")
public class AuditEntry {

    @Id
    @GeneratedValue(strategy = GenerationType.IDENTITY)
    @Column(name = "seq")
    private Long seq;

    @Column(name = "event_id", nullable = false, unique = true)
    private UUID eventId;

    @Column(name = "actor", nullable = false, columnDefinition = "text")
    private String actor;

    @Column(name = "action", nullable = false, columnDefinition = "text")
    private String action;

    @Column(name = "resource", nullable = false, columnDefinition = "text")
    private String resource;

    @Column(name = "occurred_at", nullable = false)
    private Instant occurredAt;

    @JdbcTypeCode(SqlTypes.JSON)
    @Column(name = "details", nullable = false, columnDefinition = "jsonb")
    private String details;

    @JdbcTypeCode(SqlTypes.CHAR)
    @Column(name = "prev_hash", nullable = false, columnDefinition = "char(64)")
    private String prevHash;

    @JdbcTypeCode(SqlTypes.CHAR)
    @Column(name = "hash", nullable = false, columnDefinition = "char(64)")
    private String hash;

    protected AuditEntry() {
    }

    public AuditEntry(UUID eventId, String actor, String action, String resource, Instant occurredAt,
                      String details, String prevHash, String hash) {
        this.eventId = eventId;
        this.actor = actor;
        this.action = action;
        this.resource = resource;
        this.occurredAt = occurredAt;
        this.details = details;
        this.prevHash = prevHash;
        this.hash = hash;
    }

    public HashChain.Link toLink() {
        return new HashChain.Link(seq, eventId, actor, action, resource, occurredAt, details, prevHash, hash);
    }

    public Long getSeq() {
        return seq;
    }

    public UUID getEventId() {
        return eventId;
    }

    public String getActor() {
        return actor;
    }

    public String getAction() {
        return action;
    }

    public String getResource() {
        return resource;
    }

    public Instant getOccurredAt() {
        return occurredAt;
    }

    public String getDetails() {
        return details;
    }

    public String getPrevHash() {
        return prevHash;
    }

    public String getHash() {
        return hash;
    }
}
