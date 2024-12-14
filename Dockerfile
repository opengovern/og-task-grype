# Build stage
FROM golang:alpine AS build

# Install dependencies for building and for grype db update
RUN apk --no-cache add ca-certificates curl git

# Install Grype using the official script
# If not copied locally, fetch directly:
RUN curl -sSfL https://raw.githubusercontent.com/anchore/grype/main/install.sh | sh -s -- -b /usr/local/bin

# Verify grype installation
RUN grype version

# Set environment variables so that grype downloads the DB into a known directory
ENV GRYPE_DB_CACHE_DIR=/grype-db-cache
ENV GRYPE_DB_AUTO_UPDATE=false
RUN mkdir -p $GRYPE_DB_CACHE_DIR

# Download the Grype vulnerability database at build time (airgap preparation)
# In some cases you may need --skip-tls-verify depending on your environment
RUN grype db update --cache-dir $GRYPE_DB_CACHE_DIR

# Build your Go binary (og-task-grype)
WORKDIR /app
COPY . .
RUN go build -o og-task-grype ./main.go

# Final minimal image
FROM scratch

# Copy CA certificates
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy Grype binary
COPY --from=build /usr/local/bin/grype /usr/local/bin/grype

# Copy the pre-downloaded database
COPY --from=build /grype-db-cache /grype-db-cache

# Copy your application binary
COPY --from=build /app/og-task-grype /og-task-grype

# Set environment variables so Grype uses the embedded DB and does not try to update
ENV GRYPE_DB_CACHE_DIR=/grype-db-cache
ENV GRYPE_DB_AUTO_UPDATE=false

# Set entrypoint
ENTRYPOINT ["/og-task-grype"]
