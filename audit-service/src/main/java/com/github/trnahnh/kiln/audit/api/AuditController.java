package com.github.trnahnh.kiln.audit.api;

import java.time.Instant;
import java.util.List;
import java.util.UUID;

import org.springframework.data.domain.PageRequest;
import org.springframework.data.domain.Sort;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.annotation.JsonRawValue;
import com.github.trnahnh.kiln.audit.chain.HashChain;
import com.github.trnahnh.kiln.audit.store.AuditEntry;
import com.github.trnahnh.kiln.audit.store.AuditEntryRepository;

@RestController
@RequestMapping("/v1/audit")
public class AuditController {

    static final int DEFAULT_LIMIT = 100;
    static final int MAX_LIMIT = 1000;

    private final AuditEntryRepository repository;
    private final ChainVerifier verifier;

    public AuditController(AuditEntryRepository repository, ChainVerifier verifier) {
        this.repository = repository;
        this.verifier = verifier;
    }

    public record Entry(long seq, UUID eventId, String actor, String action, String resource, String timestamp,
                        @JsonRawValue String details, String prevHash, String hash) {
        static Entry of(AuditEntry e) {
            return new Entry(e.getSeq(), e.getEventId(), e.getActor(), e.getAction(), e.getResource(),
                    HashChain.formatOccurredAt(e.getOccurredAt()), e.getDetails(), e.getPrevHash(), e.getHash());
        }
    }

    public record Query(List<Entry> entries) {
    }

    @JsonInclude(JsonInclude.Include.NON_NULL)
    public record Verify(boolean ok, String code, long entries, List<HashChain.BrokenLink> brokenLinks) {
    }

    @GetMapping
    public Query query(@RequestParam(required = false) String actor,
                       @RequestParam(required = false) String resource,
                       @RequestParam(required = false) Instant from,
                       @RequestParam(required = false) Instant to,
                       @RequestParam(required = false) Integer limit) {
        int size = limit == null ? DEFAULT_LIMIT : Math.max(1, Math.min(limit, MAX_LIMIT));
        List<Entry> entries = repository.search(actor, resource, from, to, PageRequest.of(0, size, Sort.by("seq").ascending()))
                .stream().map(Entry::of).toList();
        return new Query(entries);
    }

    @GetMapping("/verify")
    public Verify verify() {
        ChainVerifier.Verification v = verifier.verify();
        return new Verify(v.ok(), v.code(), v.entries(), v.ok() ? null : v.brokenLinks());
    }
}
