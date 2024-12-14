# Build stage
FROM golang:alpine AS build

# Install dependencies
RUN apk --no-cache add ca-certificates curl git

# Set Grype version without leading 'v' to match asset naming
ARG GRYPE_VERSION="0.86.1"

# Download and install Grype
RUN curl -sSfL "https://github.com/anchore/grype/releases/download/v${GRYPE_VERSION}/grype_${GRYPE_VERSION}_linux_amd64.tar.gz" \
    | tar -xz -C /usr/local/bin grype

# Verify grype installation
RUN /usr/local/bin/grype version

# Build og-task-grype
WORKDIR /app
COPY . .
RUN go build -o og-task-grype ./local/og-task-grype.go

# Final minimal image
FROM scratch

# Copy CA certificates
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy Grype binary
COPY --from=build /usr/local/bin/grype /usr/local/bin/grype

# Copy og-task-grype binary
COPY --from=build /app/og-task-grype /og-task-grype

# Set the entrypoint to your binary
ENTRYPOINT ["/og-task-grype"]
