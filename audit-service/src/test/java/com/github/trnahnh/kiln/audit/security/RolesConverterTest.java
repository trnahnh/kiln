package com.github.trnahnh.kiln.audit.security;

import static org.assertj.core.api.Assertions.assertThat;

import java.time.Instant;
import java.util.List;
import java.util.Map;

import org.junit.jupiter.api.Test;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.oauth2.jwt.Jwt;

class RolesConverterTest {

    private static Jwt jwt(Object roles) {
        Jwt.Builder b = Jwt.withTokenValue("t").header("alg", "RS256").subject("dev@company.com")
                .issuedAt(Instant.now()).expiresAt(Instant.now().plusSeconds(60));
        if (roles != null) {
            b.claim(RolesConverter.CLAIM, roles);
        }
        return b.build();
    }

    @Test
    void rolesClaimBecomesAuthoritiesVerbatim() {
        assertThat(new RolesConverter().convert(jwt(List.of("audit:read", "requests:submit"))))
                .extracting(GrantedAuthority::getAuthority)
                .containsExactly("audit:read", "requests:submit");
    }

    @Test
    void missingBlankOrForeignClaimsGrantNothing() {
        assertThat(new RolesConverter().convert(jwt(null))).isEmpty();
        assertThat(new RolesConverter().convert(jwt(List.of("", "  ")))).isEmpty();
        assertThat(new RolesConverter().convert(jwt(Map.of("k", "v")))).isEmpty();
        assertThat(new RolesConverter().convert(jwt(List.of(42, "audit:admin"))))
                .extracting(GrantedAuthority::getAuthority).containsExactly("audit:admin");
    }

    @Test
    void aSingleStringRoleIsAccepted() {
        assertThat(new RolesConverter().convert(jwt("audit:admin")))
                .extracting(GrantedAuthority::getAuthority).containsExactly("audit:admin");
    }
}
