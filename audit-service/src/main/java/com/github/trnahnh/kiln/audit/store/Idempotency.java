package com.github.trnahnh.kiln.audit.store;

import java.sql.SQLException;

/**
 * The dedup decision: the unique constraint on event_id is the only mechanism (CLAUDE.md
 * invariant), so a redelivery is recognised purely by the database refusing the row.
 */
public final class Idempotency {

    static final String UNIQUE_VIOLATION = "23505";
    static final String CONSTRAINT = "audit_entry_event_id_key";

    private Idempotency() {
    }

    public static boolean isDuplicate(Throwable failure) {
        for (Throwable t = failure; t != null; t = t.getCause()) {
            if (t instanceof SQLException sql && UNIQUE_VIOLATION.equals(sql.getSQLState())) {
                String message = sql.getMessage();
                return message == null || message.contains(CONSTRAINT);
            }
        }
        return false;
    }
}
