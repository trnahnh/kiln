package com.github.trnahnh.kiln.audit.store;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;

import org.springframework.data.domain.Pageable;
import org.springframework.data.jpa.domain.Specification;
import org.springframework.data.jpa.repository.JpaRepository;
import org.springframework.data.jpa.repository.JpaSpecificationExecutor;

import jakarta.persistence.criteria.Predicate;

public interface AuditEntryRepository extends JpaRepository<AuditEntry, Long>, JpaSpecificationExecutor<AuditEntry> {

    Optional<AuditEntry> findTopByOrderBySeqDesc();

    List<AuditEntry> findAllByOrderBySeqAsc();

    default List<AuditEntry> search(String actor, String resource, Instant from, Instant to, Pageable page) {
        Specification<AuditEntry> spec = (root, query, cb) -> {
            List<Predicate> where = new ArrayList<>();
            if (actor != null) {
                where.add(cb.equal(root.get("actor"), actor));
            }
            if (resource != null) {
                where.add(cb.equal(root.get("resource"), resource));
            }
            if (from != null) {
                where.add(cb.greaterThanOrEqualTo(root.get("occurredAt"), from));
            }
            if (to != null) {
                where.add(cb.lessThanOrEqualTo(root.get("occurredAt"), to));
            }
            return cb.and(where.toArray(Predicate[]::new));
        };
        return findAll(spec, page).getContent();
    }
}
