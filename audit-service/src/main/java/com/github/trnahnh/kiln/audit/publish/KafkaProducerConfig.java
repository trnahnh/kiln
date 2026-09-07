package com.github.trnahnh.kiln.audit.publish;

import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.kafka.core.KafkaTemplate;
import org.springframework.kafka.core.ProducerFactory;

import io.micrometer.observation.ObservationRegistry;

@Configuration
public class KafkaProducerConfig {

    /**
     * A template of our own, so Boot's {@code spring.kafka.template.observation-enabled}
     * does not reach it: observation is switched on here, which is what writes the W3C
     * trace headers on every record (ADR-0021).
     */
    @Bean
    @SuppressWarnings("unchecked")
    KafkaTemplate<String, byte[]> auditKafkaTemplate(ProducerFactory<?, ?> factory, ObservationRegistry observations) {
        KafkaTemplate<String, byte[]> template = new KafkaTemplate<>((ProducerFactory<String, byte[]>) factory);
        template.setObservationEnabled(true);
        template.setObservationRegistry(observations);
        return template;
    }
}
