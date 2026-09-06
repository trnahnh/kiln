package com.github.trnahnh.kiln.audit.ingest;

import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.kafka.listener.CommonErrorHandler;
import org.springframework.kafka.listener.DefaultErrorHandler;
import org.springframework.util.backoff.FixedBackOff;

@Configuration
public class KafkaListenerConfig {

    /**
     * A record the database refuses for any reason other than a duplicate is retried forever
     * rather than skipped: losing an audit entry to a transient outage is worse than a stalled
     * partition, and the offset is only committed after the transaction commits.
     */
    @Bean
    CommonErrorHandler auditErrorHandler() {
        return new DefaultErrorHandler(new FixedBackOff(1_000L, FixedBackOff.UNLIMITED_ATTEMPTS));
    }
}
