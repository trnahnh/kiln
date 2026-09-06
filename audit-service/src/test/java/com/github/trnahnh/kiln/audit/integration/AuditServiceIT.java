package com.github.trnahnh.kiln.audit.integration;

import static org.assertj.core.api.Assertions.assertThat;
import static org.awaitility.Awaitility.await;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.when;

import java.nio.charset.StandardCharsets;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Tag;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;
import org.junit.jupiter.api.io.TempDir;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.web.server.LocalServerPort;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.kafka.core.KafkaTemplate;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.web.client.HttpClientErrorException;
import org.springframework.web.client.RestClient;
import org.testcontainers.junit.jupiter.Container;
import org.testcontainers.junit.jupiter.Testcontainers;
import org.testcontainers.kafka.KafkaContainer;
import org.testcontainers.postgresql.PostgreSQLContainer;

import com.github.trnahnh.kiln.audit.chain.CanonicalJson;
import com.github.trnahnh.kiln.audit.chain.HashChain;
import com.github.trnahnh.kiln.audit.requests.AdmissionRejectedException;
import com.github.trnahnh.kiln.audit.requests.ManifestApplier;

import io.fabric8.kubernetes.api.model.GenericKubernetesResource;
import io.fabric8.kubernetes.api.model.ObjectMeta;
import io.fabric8.kubernetes.client.KubernetesClient;
import io.micrometer.core.instrument.MeterRegistry;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;

/**
 * The Phase 6 exit criterion against real Kafka and Postgres. Rows are read with plain JDBC,
 * never through the service's own endpoints, and tampering is done underneath it.
 */
@Tag("integration")
@Testcontainers
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
class AuditServiceIT {

    static final String TOPIC = "kiln.audit";

    @Container
    static final PostgreSQLContainer postgres = new PostgreSQLContainer("postgres:16");

    @Container
    static final KafkaContainer kafka = new KafkaContainer("apache/kafka:3.9.1");

    @TempDir
    static Path keys;
    static TestJwt jwt;

    @BeforeAll
    static void issuer() throws Exception {
        jwt = new TestJwt(keys);
    }

    @DynamicPropertySource
    static void wire(DynamicPropertyRegistry r) {
        r.add("spring.datasource.url", postgres::getJdbcUrl);
        r.add("spring.datasource.username", postgres::getUsername);
        r.add("spring.datasource.password", postgres::getPassword);
        r.add("spring.kafka.bootstrap-servers", kafka::getBootstrapServers);
        r.add("kiln.audit.topic", () -> TOPIC);
        r.add("spring.security.oauth2.resourceserver.jwt.public-key-location", () -> "file:" + jwt.publicKeyPem.toAbsolutePath());
    }

    @MockitoBean
    ManifestApplier applier;

    @MockitoBean
    KubernetesClient kubernetesClient;

    @Autowired
    JdbcTemplate jdbc;

    @Autowired
    KafkaTemplate<String, byte[]> producer;

    @Autowired
    MeterRegistry meters;

    @LocalServerPort
    int port;

    static final JsonMapper json = CanonicalJson.mapper();
    static final List<byte[]> published = new ArrayList<>();
    static final Instant T0 = Instant.parse("2026-09-06T10:00:00Z");

    RestClient client() {
        return RestClient.builder().baseUrl("http://localhost:" + port).build();
    }

    static byte[] wire(UUID id, String actor, String action, String resource, Instant at, String details) {
        String s = "{\"eventId\":\"" + id + "\",\"actor\":\"" + actor + "\",\"action\":\"" + action
                + "\",\"resource\":\"" + resource + "\",\"timestamp\":\"" + at + "\",\"details\":" + details + "}";
        return s.getBytes(StandardCharsets.UTF_8);
    }

    void publishRaw(String key, byte[] value) throws Exception {
        producer.send(TOPIC, key, value).get();
    }

    long rows() {
        Long n = jdbc.queryForObject("select count(*) from audit_entry", Long.class);
        return n == null ? 0 : n;
    }

    List<HashChain.Link> links() {
        return jdbc.query("select seq, event_id, actor, action, resource, occurred_at, details::text, prev_hash, hash from audit_entry order by seq",
                (rs, i) -> new HashChain.Link(rs.getLong(1), rs.getObject(2, UUID.class), rs.getString(3), rs.getString(4),
                        rs.getString(5), rs.getObject(6, java.time.OffsetDateTime.class).toInstant(), rs.getString(7),
                        rs.getString(8), rs.getString(9)));
    }

    double counter(String name) {
        var c = meters.find(name).counter();
        return c == null ? 0 : c.count();
    }

    @Test
    @Order(1)
    void everyPublishedEventBecomesOneChainedRow() throws Exception {
        String[] actions = {"PROVISION_REQUEST", "PROVISION", "BACKUP", "DEPLOY", "CHAOS_EXPERIMENT"};
        for (int i = 0; i < actions.length; i++) {
            String resource = "TenantDatabase/team-checkout/db" + i;
            String actor = i % 2 == 0 ? "dev@company.com" : "system:operator";
            byte[] value = wire(UUID.nameUUIDFromBytes(("it-" + i).getBytes(StandardCharsets.UTF_8)), actor, actions[i],
                    resource, T0.plusSeconds(60L * i).plusNanos(123_456_789), "{\"z\":" + i + ",\"outcome\":\"Ready\",\"nested\":{\"b\":2,\"a\":1.50}}");
            published.add(value);
            publishRaw(resource, value);
        }
        await().atMost(Duration.ofSeconds(60)).until(() -> rows() == 5);

        List<HashChain.Link> chain = links();
        assertThat(chain).hasSize(5);
        assertThat(chain.get(0).prevHash()).isEqualTo(HashChain.GENESIS);
        for (int i = 1; i < chain.size(); i++) {
            assertThat(chain.get(i).prevHash()).isEqualTo(chain.get(i - 1).hash());
        }
        for (HashChain.Link link : chain) {
            assertThat(HashChain.hash(link)).as("row %d hash recomputed from stored content", link.seq()).isEqualTo(link.hash());
        }
        assertThat(chain.get(0).occurredAt()).isEqualTo(T0.plusNanos(123_456_000));
        assertThat(chain.get(0).details()).contains("\"nested\"");
        assertThat(HashChain.verify(chain)).isEmpty();
    }

    @Test
    @Order(2)
    void aRedeliveredRecordIsAbsorbedByTheUniqueConstraint() throws Exception {
        double before = counter("kiln_audit_duplicates_total");
        publishRaw("TenantDatabase/team-checkout/db2", published.get(2));
        publishRaw("TenantDatabase/team-checkout/db0", published.get(0));
        await().atMost(Duration.ofSeconds(60)).until(() -> counter("kiln_audit_duplicates_total") >= before + 2);
        assertThat(rows()).isEqualTo(5);
        assertThat(HashChain.verify(links())).isEmpty();
        assertThat(jdbc.queryForObject("select count(distinct event_id) from audit_entry", Long.class)).isEqualTo(5L);
    }

    @Test
    @Order(3)
    void aPoisonRecordIsSkippedAndTheNextOneStillLands() throws Exception {
        double rejected = counter("kiln_audit_rejected_total");
        publishRaw("junk", "not json at all".getBytes(StandardCharsets.UTF_8));
        publishRaw("junk", "{\"eventId\":\"nope\"}".getBytes(StandardCharsets.UTF_8));
        byte[] value = wire(UUID.nameUUIDFromBytes("after-poison".getBytes(StandardCharsets.UTF_8)), "dev@company.com", "ROLLBACK",
                "CanaryRollout/team-checkout/svc", T0.plusSeconds(600), "{\"outcome\":\"RolledBack\"}");
        published.add(value);
        publishRaw("CanaryRollout/team-checkout/svc", value);
        await().atMost(Duration.ofSeconds(60)).until(() -> rows() == 6);
        assertThat(counter("kiln_audit_rejected_total")).isEqualTo(rejected + 2);
        assertThat(HashChain.verify(links())).isEmpty();
    }

    @Test
    @Order(4)
    void endpointsAreGuardedByRoles() {
        RestClient c = client();
        assertThat(c.get().uri("/healthz").retrieve().body(String.class)).contains("ok");

        for (String path : new String[] {"/v1/audit", "/v1/audit/verify"}) {
            assertThat(status(() -> c.get().uri(path).retrieve().toBodilessEntity())).isEqualTo(HttpStatus.UNAUTHORIZED);
        }
        String reader = jwt.token("reader@company.com", List.of("audit:read"));
        assertThat(status(() -> c.get().uri("/v1/audit/verify").header("Authorization", "Bearer " + reader).retrieve().toBodilessEntity()))
                .isEqualTo(HttpStatus.FORBIDDEN);
        assertThat(status(() -> c.post().uri("/v1/requests").header("Authorization", "Bearer " + reader)
                .contentType(MediaType.APPLICATION_JSON).body("{}").retrieve().toBodilessEntity()))
                .isEqualTo(HttpStatus.FORBIDDEN);
        String admin = jwt.token("admin@company.com", List.of("audit:admin"));
        assertThat(status(() -> c.get().uri("/v1/audit").header("Authorization", "Bearer " + admin).retrieve().toBodilessEntity()))
                .isEqualTo(HttpStatus.FORBIDDEN);
    }

    @Test
    @Order(5)
    void queryFiltersByActorResourceAndTime() {
        String reader = jwt.token("reader@company.com", List.of("audit:read"));
        JsonNode all = get("/v1/audit", reader);
        assertThat(all.get("entries")).hasSize(6);
        assertThat(all.get("entries").get(0).get("seq").asLong()).isEqualTo(1);
        assertThat(all.get("entries").get(0).get("prevHash").asString()).isEqualTo(HashChain.GENESIS);
        assertThat(all.get("entries").get(0).get("details").get("nested").get("a").decimalValue().toPlainString()).isEqualTo("1.50");

        assertThat(get("/v1/audit?actor=system:operator", reader).get("entries")).hasSize(2);
        assertThat(get("/v1/audit?resource=TenantDatabase/team-checkout/db3", reader).get("entries")).hasSize(1);
        assertThat(get("/v1/audit?from=2026-09-06T10:01:00Z&to=2026-09-06T10:03:01Z", reader).get("entries")).hasSize(3);
        assertThat(get("/v1/audit?limit=2", reader).get("entries")).hasSize(2);
        assertThat(get("/v1/audit?actor=nobody", reader).get("entries")).isEmpty();
    }

    @Test
    @Order(6)
    void verifyPassesOnAnIntactChainAndNamesATamperedRow() {
        String admin = jwt.token("admin@company.com", List.of("audit:admin"));
        JsonNode ok = get("/v1/audit/verify", admin);
        assertThat(ok.get("ok").asBoolean()).isTrue();
        assertThat(ok.get("entries").asLong()).isEqualTo(6);

        assertThat(jdbc.update("update audit_entry set actor = 'mallory' where seq = 2")).isEqualTo(1);
        JsonNode broken = get("/v1/audit/verify", admin);
        assertThat(broken.get("ok").asBoolean()).isFalse();
        assertThat(broken.get("code").asString()).isEqualTo("AUDIT_CHAIN_BROKEN");
        assertThat(broken.get("brokenLinks")).hasSize(1);
        assertThat(broken.get("brokenLinks").get(0).get("seq").asLong()).isEqualTo(2);
        assertThat(broken.get("brokenLinks").get(0).get("reason").asString()).isEqualTo("hash mismatch");

        // A forger who also recomputes the row's hash still breaks the next link.
        HashChain.Link forged = links().get(1);
        jdbc.update("update audit_entry set hash = ? where seq = 2", HashChain.hash(forged));
        JsonNode next = get("/v1/audit/verify", admin);
        assertThat(next.get("ok").asBoolean()).isFalse();
        assertThat(next.get("brokenLinks").get(0).get("seq").asLong()).isEqualTo(3);
        assertThat(next.get("brokenLinks").get(0).get("reason").asString()).isEqualTo("prevHash mismatch");

        jdbc.update("update audit_entry set actor = ?, hash = ? where seq = 2", "system:operator", forged.prevHash().equals(HashChain.GENESIS) ? forged.hash() : restoreHash(forged));
        assertThat(get("/v1/audit/verify", admin).get("ok").asBoolean()).isTrue();
    }

    private String restoreHash(HashChain.Link tampered) {
        return HashChain.hash(tampered.prevHash(), tampered.eventId(), "system:operator", tampered.action(), tampered.resource(),
                tampered.occurredAt(), tampered.details());
    }

    @Test
    @Order(7)
    void submittedRequestsAreAuditedThroughKafkaNotDirectly() throws Exception {
        GenericKubernetesResource applied = new GenericKubernetesResource();
        applied.setKind("DatabaseClaim");
        ObjectMeta meta = new ObjectMeta();
        meta.setName("checkout-db");
        meta.setNamespace("team-checkout");
        meta.setResourceVersion("99");
        applied.setMetadata(meta);
        when(applier.apply(any(), any())).thenReturn(applied);

        String submitter = jwt.token("dev@company.com", List.of("requests:submit"));
        long before = rows();
        JsonNode accepted = json.readTree(client().post().uri("/v1/requests")
                .header("Authorization", "Bearer " + submitter).contentType(MediaType.APPLICATION_JSON)
                .body("{\"apiVersion\":\"platform.internal/v1alpha1\",\"kind\":\"DatabaseClaim\",\"metadata\":{\"name\":\"checkout-db\",\"namespace\":\"team-checkout\"},\"spec\":{\"parameters\":{\"storageGB\":20}}}")
                .retrieve().body(String.class));
        assertThat(accepted.get("resource").asString()).isEqualTo("DatabaseClaim/team-checkout/checkout-db");

        await().atMost(Duration.ofSeconds(60)).until(() -> rows() == before + 1);
        Map<String, Object> row = jdbc.queryForMap("select actor, action, resource, details::text as details from audit_entry order by seq desc limit 1");
        assertThat(row).containsEntry("actor", "dev@company.com").containsEntry("action", "PROVISION_REQUEST")
                .containsEntry("resource", "DatabaseClaim/team-checkout/checkout-db");
        assertThat((String) row.get("details")).contains("\"outcome\": \"Received\"");

        when(applier.apply(any(), any())).thenThrow(new AdmissionRejectedException(400,
                "admission webhook \"validate.kyverno.svc-fail\" denied the request: POLICY_DENIED rule=tags-required: tags.team is mandatory", null));
        HttpStatus denied = status(() -> client().post().uri("/v1/requests")
                .header("Authorization", "Bearer " + submitter).contentType(MediaType.APPLICATION_JSON)
                .body("{\"kind\":\"DatabaseClaim\",\"metadata\":{\"name\":\"bad\",\"namespace\":\"team-checkout\"},\"spec\":{}}")
                .retrieve().toBodilessEntity());
        assertThat(denied).isEqualTo(HttpStatus.UNPROCESSABLE_CONTENT);
        await().atMost(Duration.ofSeconds(60)).until(() -> rows() == before + 2);
        Map<String, Object> deny = jdbc.queryForMap("select action, details::text as details from audit_entry order by seq desc limit 1");
        assertThat(deny).containsEntry("action", "POLICY_DENY");
        assertThat((String) deny.get("details")).contains("tags-required");

        assertThat(status(() -> client().post().uri("/v1/requests")
                .header("Authorization", "Bearer " + submitter).contentType(MediaType.APPLICATION_JSON)
                .body("{\"kind\":\"Deployment\",\"metadata\":{\"name\":\"x\"}}").retrieve().toBodilessEntity()))
                .isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(HashChain.verify(links())).isEmpty();
    }

    private JsonNode get(String path, String token) {
        return json.readTree(client().get().uri(path).header("Authorization", "Bearer " + token).retrieve().body(String.class));
    }

    private static HttpStatus status(Runnable call) {
        try {
            call.run();
            return HttpStatus.OK;
        } catch (HttpClientErrorException e) {
            return HttpStatus.valueOf(e.getStatusCode().value());
        }
    }
}
