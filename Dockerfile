# Build stage
FROM alpine:3.18 AS build

# Install dependencies for building and extracting
RUN apk --no-cache add ca-certificates curl git tar

# Install Grype (latest stable)
RUN curl -sSfL https://raw.githubusercontent.com/anchore/grype/main/install.sh | sh -s -- -b /usr/local/bin
RUN grype version

# Download and embed a known Grype database
ARG GRYPE_DB_URL="https://grype.anchore.io/databases/vulnerability-db_v5_2024-12-14T01:31:37Z_1734150182.tar.gz"
ARG GRYPE_DB_CACHE_DIR="/grype-db-cache"
RUN mkdir -p $GRYPE_DB_CACHE_DIR
RUN curl -sSfL "$GRYPE_DB_URL" | tar -xz -C $GRYPE_DB_CACHE_DIR

# Build og-task-grype binary
WORKDIR /app
COPY . .
RUN go build -o og-task-grype main.go

# Final minimal image
FROM scratch

# Copy CA certificates
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy Grype binary
COPY --from=build /usr/local/bin/grype /usr/local/bin/grype

# Copy og-task-grype binary
COPY --from=build /app/og-task-grype /og-task-grype

# Copy the downloaded database
COPY --from=build /grype-db-cache /grype-db-cache

# Disable auto-updates and set DB directory
ENV GRYPE_DB_AUTO_UPDATE=false
ENV GRYPE_DB_CACHE_DIR=/grype-db-cache

ENTRYPOINT ["/og-task-grype"]
