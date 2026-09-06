package com.github.trnahnh.kiln.audit.ingest;

import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.dao.DataAccessException;
import org.springframework.kafka.annotation.KafkaListener;
import org.springframework.kafka.support.Acknowledgment;
import org.springframework.stereotype.Component;

import com.github.trnahnh.kiln.audit.event.MalformedEventException;
import com.github.trnahnh.kiln.audit.event.WireEvent;
import com.github.trnahnh.kiln.audit.event.WireEventCodec;
import com.github.trnahnh.kiln.audit.store.AuditWriter;
import com.github.trnahnh.kiln.audit.store.Idempotency;

import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.MeterRegistry;

@Component
public class AuditConsumer {

    private static final Logger log = LoggerFactory.getLogger(AuditConsumer.class);

    private final AuditWriter writer;
    private final Counter stored;
    private final Counter duplicates;
    private final Counter rejected;

    public AuditConsumer(AuditWriter writer, MeterRegistry registry) {
        this.writer = writer;
        this.stored = registry.counter("kiln_audit_stored_total");
        this.duplicates = registry.counter("kiln_audit_duplicates_total");
        this.rejected = registry.counter("kiln_audit_rejected_total");
    }

    @KafkaListener(topics = "${kiln.audit.topic}", groupId = "kiln-audit-service", concurrency = "1")
    public void consume(ConsumerRecord<String, byte[]> record, Acknowledgment ack) {
        WireEvent event;
        try {
            event = WireEventCodec.decode(record.value());
        } catch (MalformedEventException e) {
            rejected.increment();
            log.warn("rejecting unparseable audit record at {}-{}@{}: {}", record.topic(), record.partition(),
                    record.offset(), e.getMessage());
            ack.acknowledge();
            return;
        }
        try {
            writer.append(event);
            stored.increment();
        } catch (DataAccessException e) {
            if (!Idempotency.isDuplicate(e)) {
                throw e;
            }
            duplicates.increment();
            log.info("duplicate delivery of {} for {} absorbed by the unique constraint", event.eventId(),
                    event.resource());
        }
        ack.acknowledge();
    }
}
