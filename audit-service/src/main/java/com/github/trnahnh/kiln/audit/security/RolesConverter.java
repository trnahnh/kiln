package com.github.trnahnh.kiln.audit.security;

import java.util.ArrayList;
import java.util.Collection;
import java.util.List;

import org.springframework.core.convert.converter.Converter;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.authority.SimpleGrantedAuthority;
import org.springframework.security.oauth2.jwt.Jwt;

/** The roles claim, verbatim, is the authority set; no ROLE_ prefix, no scopes. */
public final class RolesConverter implements Converter<Jwt, Collection<GrantedAuthority>> {

    public static final String CLAIM = "roles";

    @Override
    public Collection<GrantedAuthority> convert(Jwt jwt) {
        List<GrantedAuthority> authorities = new ArrayList<>();
        Object claim = jwt.getClaims().get(CLAIM);
        if (claim instanceof Collection<?> roles) {
            for (Object role : roles) {
                if (role instanceof String s && !s.isBlank()) {
                    authorities.add(new SimpleGrantedAuthority(s));
                }
            }
        } else if (claim instanceof String s && !s.isBlank()) {
            authorities.add(new SimpleGrantedAuthority(s));
        }
        return authorities;
    }
}
