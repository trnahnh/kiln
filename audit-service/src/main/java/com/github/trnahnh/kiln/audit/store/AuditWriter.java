package com.github.trnahnh.kiln.audit.store;

import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

import com.github.trnahnh.kiln.audit.chain.HashChain;
import com.github.trnahnh.kiln.audit.event.WireEvent;

import jakarta.persistence.EntityManager;

/** The one insert path; the advisory lock serialises chain extension (DATA_MODEL.md). */
@Service
public class AuditWriter {

    private final AuditEntryRepository repository;
    private final EntityManager entityManager;

    public AuditWriter(AuditEntryRepository repository, EntityManager entityManager) {
        this.repository = repository;
        this.entityManager = entityManager;
    }

    @Transactional
    public AuditEntry append(WireEvent event) {
        entityManager.createNativeQuery("select pg_advisory_xact_lock(1)").getSingleResult();
        String prevHash = repository.findTopByOrderBySeqDesc().map(AuditEntry::getHash).orElse(HashChain.GENESIS);
        String hash = HashChain.hash(prevHash, event.eventId(), event.actor(), event.action(), event.resource(),
                event.timestamp(), event.details());
        AuditEntry entry = new AuditEntry(event.eventId(), event.actor(), event.action(), event.resource(),
                event.timestamp(), event.details(), prevHash, hash);
        return repository.saveAndFlush(entry);
    }
}
