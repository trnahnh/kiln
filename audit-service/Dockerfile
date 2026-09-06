FROM eclipse-temurin:21-jdk AS builder
WORKDIR /workspace
COPY gradlew settings.gradle.kts build.gradle.kts ./
COPY gradle gradle
RUN ./gradlew --no-daemon -q dependencies > /dev/null
COPY src src
RUN ./gradlew --no-daemon -q bootJar -x test

FROM eclipse-temurin:21-jre
WORKDIR /
COPY --from=builder /workspace/build/libs/audit-service-*.jar /audit-service.jar
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["java", "-jar", "/audit-service.jar"]
