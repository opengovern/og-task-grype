# Build stage
FROM golang:1.21-alpine AS build

# Install dependencies for downloading and extracting Grype DB
RUN apk --no-cache add ca-certificates curl git tar

# Install Grype
RUN curl -sSfL https://raw.githubusercontent.com/anchore/grype/main/install.sh | sh -s -- -b /usr/local/bin

# Verify grype installation
RUN grype version

# Download and extract the database
ARG GRYPE_DB_URL="https://grype.anchore.io/databases/vulnerability-db_v5_2024-12-14T01:31:37Z_1734150182.tar.gz"
RUN mkdir -p /grype-db-cache
RUN curl -sSfL "$GRYPE_DB_URL" | tar -xz -C /grype-db-cache

# Build your Go binary
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

# Copy the database
COPY --from=build /grype-db-cache /grype-db-cache

# Disable auto-update
ENV GRYPE_DB_AUTO_UPDATE=false
ENV GRYPE_DB_CACHE_DIR=/grype-db-cache

ENTRYPOINT ["/og-task-grype"]
