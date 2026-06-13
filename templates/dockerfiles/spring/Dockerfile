# =============================================================================
# Spring Boot (Java) — production multi-stage Dockerfile (starter; edit me)
# Stage 1: build the fat jar with Maven.  Stage 2: lean JRE runtime.
# Gradle projects: swap Stage 1 for `gradle:8-jdk21` + `gradle bootJar`.
# =============================================================================

# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM maven:3.9-eclipse-temurin-21 AS builder

WORKDIR /app

# Dependency layer: resolve offline before copying sources for better caching.
COPY pom.xml ./
RUN mvn -B -q dependency:go-offline

COPY src ./src
RUN mvn -B -q clean package -DskipTests

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM eclipse-temurin:21-jre AS runtime

RUN groupadd -r appgroup && useradd -r -g appgroup appuser

WORKDIR /app
# The Spring Boot plugin produces exactly one bootable jar under target/.
COPY --from=builder /app/target/*.jar app.jar

ENV PORT=8080 \
    JAVA_OPTS=""
EXPOSE 8080

ARG VERSION=0.0.0
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.title="spring-app"

USER appuser

HEALTHCHECK --interval=30s --timeout=5s --start-period=40s --retries=3 \
  CMD wget -qO- http://localhost:8080/actuator/health >/dev/null 2>&1 \
      || curl -fsS http://localhost:8080/actuator/health >/dev/null 2>&1 || exit 1

ENTRYPOINT ["sh", "-c", "java $JAVA_OPTS -Dserver.port=${PORT} -jar app.jar"]
