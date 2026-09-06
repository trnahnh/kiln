package com.github.trnahnh.kiln.audit.store;

import static org.assertj.core.api.Assertions.assertThat;

import java.sql.SQLException;

import org.junit.jupiter.api.Test;
import org.springframework.dao.DataIntegrityViolationException;

class IdempotencyTest {

    @Test
    void uniqueViolationOnEventIdIsADuplicate() {
        SQLException sql = new SQLException("ERROR: duplicate key value violates unique constraint \"audit_entry_event_id_key\"", "23505");
        RuntimeException wrapped = new DataIntegrityViolationException("insert failed", new RuntimeException("jpa", sql));
        assertThat(Idempotency.isDuplicate(wrapped)).isTrue();
    }

    @Test
    void uniqueViolationOnAnotherConstraintIsNot() {
        SQLException sql = new SQLException("duplicate key value violates unique constraint \"other_key\"", "23505");
        assertThat(Idempotency.isDuplicate(new DataIntegrityViolationException("x", sql))).isFalse();
    }

    @Test
    void otherSqlStatesAndPlainFailuresAreNot() {
        assertThat(Idempotency.isDuplicate(new SQLException("connection refused", "08001"))).isFalse();
        assertThat(Idempotency.isDuplicate(new IllegalStateException("boom"))).isFalse();
        assertThat(Idempotency.isDuplicate(null)).isFalse();
    }

    @Test
    void walksTheWholeCauseChain() {
        SQLException sql = new SQLException("audit_entry_event_id_key", "23505");
        RuntimeException a = new RuntimeException("a", new RuntimeException("b", new RuntimeException("c", sql)));
        assertThat(Idempotency.isDuplicate(a)).isTrue();
    }
}
