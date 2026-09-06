CREATE TABLE audit_entry (
    seq         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id    uuid        NOT NULL UNIQUE,
    actor       text        NOT NULL,
    action      text        NOT NULL,
    resource    text        NOT NULL,
    occurred_at timestamptz NOT NULL,
    details     jsonb       NOT NULL,
    prev_hash   char(64)    NOT NULL,
    hash        char(64)    NOT NULL
);

CREATE INDEX audit_entry_actor_idx    ON audit_entry (actor, occurred_at);
CREATE INDEX audit_entry_resource_idx ON audit_entry (resource, occurred_at);
CREATE INDEX audit_entry_time_idx     ON audit_entry (occurred_at);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_reader') THEN
        GRANT SELECT ON audit_entry TO audit_reader;
    END IF;
END
$$;
