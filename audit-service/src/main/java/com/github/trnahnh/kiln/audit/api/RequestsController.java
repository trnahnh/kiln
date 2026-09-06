package com.github.trnahnh.kiln.audit.api;

import java.util.Map;
import java.util.UUID;

import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.security.core.annotation.AuthenticationPrincipal;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RestController;

import com.fasterxml.jackson.annotation.JsonInclude;
import com.github.trnahnh.kiln.audit.requests.RequestSubmitter;

import io.fabric8.kubernetes.api.model.GenericKubernetesResource;

@RestController
public class RequestsController {

    public static final String CODE_POLICY_DENIED = "POLICY_DENIED";

    private final RequestSubmitter submitter;

    public RequestsController(RequestSubmitter submitter) {
        this.submitter = submitter;
    }

    public record Accepted(String resource, UUID eventId) {
    }

    @JsonInclude(JsonInclude.Include.NON_NULL)
    public record Denied(String code, String resource, UUID eventId, String rule, String reason) {
    }

    @PostMapping("/v1/requests")
    public ResponseEntity<Accepted> submit(@RequestBody GenericKubernetesResource manifest, @AuthenticationPrincipal Jwt jwt) {
        RequestSubmitter.Accepted accepted = submitter.submit(manifest, jwt.getSubject());
        return ResponseEntity.status(HttpStatus.ACCEPTED).body(new Accepted(accepted.resource(), accepted.eventId()));
    }

    @ExceptionHandler(RequestSubmitter.DeniedException.class)
    public ResponseEntity<Denied> denied(RequestSubmitter.DeniedException e) {
        RequestSubmitter.Denied d = e.denied();
        return ResponseEntity.status(HttpStatus.UNPROCESSABLE_CONTENT)
                .body(new Denied(CODE_POLICY_DENIED, d.resource(), d.eventId(), d.rule(), d.reason()));
    }

    @ExceptionHandler(RequestSubmitter.UnsupportedKindException.class)
    public ResponseEntity<Map<String, String>> unsupported(RequestSubmitter.UnsupportedKindException e) {
        return ResponseEntity.badRequest().body(Map.of("code", "UNSUPPORTED_REQUEST", "reason", e.getMessage()));
    }
}
