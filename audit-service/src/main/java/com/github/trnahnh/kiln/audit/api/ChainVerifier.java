package com.github.trnahnh.kiln.audit.api;

import java.util.List;

import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

import com.github.trnahnh.kiln.audit.chain.HashChain;
import com.github.trnahnh.kiln.audit.store.AuditEntry;
import com.github.trnahnh.kiln.audit.store.AuditEntryRepository;

@Service
public class ChainVerifier {

    public static final String CODE_BROKEN = "AUDIT_CHAIN_BROKEN";

    private final AuditEntryRepository repository;

    public ChainVerifier(AuditEntryRepository repository) {
        this.repository = repository;
    }

    public record Verification(boolean ok, String code, long entries, List<HashChain.BrokenLink> brokenLinks) {
    }

    @Transactional(readOnly = true)
    public Verification verify() {
        List<HashChain.Link> links = repository.findAllByOrderBySeqAsc().stream().map(AuditEntry::toLink).toList();
        List<HashChain.BrokenLink> broken = HashChain.verify(links);
        return new Verification(broken.isEmpty(), broken.isEmpty() ? null : CODE_BROKEN, links.size(), broken);
    }
}
