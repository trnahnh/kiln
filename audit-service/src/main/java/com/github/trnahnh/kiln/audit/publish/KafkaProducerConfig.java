package com.github.trnahnh.kiln.audit.publish;

import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.kafka.core.KafkaTemplate;
import org.springframework.kafka.core.ProducerFactory;

@Configuration
public class KafkaProducerConfig {

    @Bean
    @SuppressWarnings("unchecked")
    KafkaTemplate<String, byte[]> auditKafkaTemplate(ProducerFactory<?, ?> factory) {
        return new KafkaTemplate<>((ProducerFactory<String, byte[]>) factory);
    }
}
