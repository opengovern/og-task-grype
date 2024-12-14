# Build stage
FROM docker.io/golang:alpine as build

# Install dependencies
RUN apk --no-cache add ca-certificates curl

# Download and install Grype
RUN curl -sSfL https://github.com/anchore/grype/releases/download/v0.73.0/grype_0.73.0_linux_amd64.tar.gz | tar -xz -C /usr/local/bin grype

# Verify installation
RUN grype version

# Final minimal image
FROM scratch

# Copy CA certificates
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy Grype binary
COPY --from=build /usr/local/bin/grype /usr/local/bin/

# Copy your binary
COPY ./local/og-task-grype ./

# Set the entrypoint
ENTRYPOINT [ "./og-task-grype" ]
CMD [ "./og-task-grype" ]