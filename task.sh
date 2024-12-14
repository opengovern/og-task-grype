#!/bin/bash

grype version

grype ghcr.io/opengovern/steampipe-plugin-aws:v0.1.6 --scope all-layers -o cyclonedx-json
grype nginx:latest --scope all-layers -o cyclonedx-json