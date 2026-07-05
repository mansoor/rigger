# =============================================================================
# Spring Boot (Java, Gradle) — production multi-stage Dockerfile (starter; edit me)
# Stage 1: build the bootable jar with Gradle.  Stage 2: lean JRE runtime.
# Maven projects: use the "spring" template instead (mvn clean package).
# =============================================================================

# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM gradle:8-jdk21 AS builder

WORKDIR /app

# Dependency layer: resolve before copying sources for better caching. The build
# files alone rarely change, so this layer is reused across source edits.
COPY build.gradle* settings.gradle* gradle.properties* ./
RUN gradle --no-daemon dependencies >/dev/null 2>&1 || true

COPY src ./src
RUN gradle --no-daemon -x test clean bootJar

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM eclipse-temurin:21-jre AS runtime

RUN groupadd -r appgroup && useradd -r -g appgroup appuser

WORKDIR /app
# The Spring Boot Gradle plugin writes the bootable jar to build/libs/ (the plain
# jar, if any, ends in -plain.jar — exclude it so exactly one jar is copied).
COPY --from=builder /app/build/libs/*.jar ./
RUN rm -f *-plain.jar && mv "$(ls *.jar | head -n1)" app.jar

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
