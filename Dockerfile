# Build stage
FROM golang:alpine AS build

# Install dependencies
RUN apk --no-cache add ca-certificates curl git

# Install Grype using the official script
RUN curl -sSfL https://raw.githubusercontent.com/anchore/grype/main/install.sh | sh -s -- -b /usr/local/bin

# Verify Grype installation
RUN /usr/local/bin/grype version

# Set the working directory and copy all files
WORKDIR /app
COPY . .

# Build your Go binary from the main package at root level
# This assumes main.go is the entrypoint of your Go application
RUN go build -o og-task-grype .

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
