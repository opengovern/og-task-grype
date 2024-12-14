#!/bin/bash

grype version

curl -sSfL https://raw.githubusercontent.com/anchore/grype/main/install.sh | sh -s -- -b /usr/local/bin

grype version

grype ghcr.io/opengovern/steampipe-plugin-aws:v0.1.6 --scope all-layers -o cyclonedx-json
grype nginx:latest --scope all-layers -o cyclonedx-json