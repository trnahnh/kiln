package com.github.trnahnh.kiln.audit.publish;

import java.util.concurrent.ExecutionException;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.kafka.core.KafkaTemplate;
import org.springframework.stereotype.Component;

import com.github.trnahnh.kiln.audit.event.WireEvent;
import com.github.trnahnh.kiln.audit.event.WireEventCodec;

/**
 * The service's own events go through the same topic as everyone else's and are stored only
 * when consumed (ADR-0018).
 */
@Component
public class EventPublisher {

    private final KafkaTemplate<String, byte[]> kafka;
    private final String topic;

    public EventPublisher(KafkaTemplate<String, byte[]> kafka, @Value("${kiln.audit.topic}") String topic) {
        this.kafka = kafka;
        this.topic = topic;
    }

    public void publish(WireEvent event) {
        try {
            kafka.send(topic, event.resource(), WireEventCodec.encode(event)).get();
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new PublishException("interrupted publishing " + event.eventId(), e);
        } catch (ExecutionException e) {
            throw new PublishException("publishing " + event.eventId() + " to " + topic, e.getCause());
        }
    }

    public static class PublishException extends RuntimeException {
        public PublishException(String message, Throwable cause) {
            super(message, cause);
        }
    }
}
