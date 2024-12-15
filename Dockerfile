# Build stage
FROM golang:1.23-alpine AS build

# Install required dependencies
RUN apk --no-cache add ca-certificates curl git tar

# Install Grype (installs latest stable by default)
RUN curl -sSfL https://raw.githubusercontent.com/anchore/grype/main/install.sh | sh -s -- -b /usr/local/bin
RUN grype version

# Download and place the Grype database in the default location
ARG GRYPE_DB_URL="https://grype.anchore.io/databases/vulnerability-db_v5_2024-12-14T01:31:37Z_1734150182.tar.gz"
RUN mkdir -p /.cache/grype/db/5
RUN curl -sSfL "$GRYPE_DB_URL" | tar -xz -C /.cache/grype/db/5

# Create a /tmp directory since scratch doesn't have one
RUN chmod 1777 /tmp

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

# Copy /tmp directory
COPY --from=build /tmp /tmp

# Copy og-task-grype binary
COPY --from=build /app/og-task-grype /og-task-grype

# Copy the database into the default location
COPY --from=build /.cache/grype/db /.cache/grype/db

# Disable auto-updates
ENV GRYPE_DB_AUTO_UPDATE=false

# Set a generic entrypoint to Grype so any arguments can be passed at runtime
ENTRYPOINT ["/usr/local/bin/grype"]
