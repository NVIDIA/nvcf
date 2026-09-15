# ADR001 Convert Provider to Go

## Context

For the first iteration of the provider (see `bin/generic-credential-provider`) the simplest approach was to use a bash script to extract the required data and provide an appropriately structured response. This kept the logic itself relatively easy to inspect when actually present on the target cluster.

For the next iteration, this provider should be ported to Golang. This will provide static typing, a robust testing framework, and as a result will make the code easier to read and understand - facilitating further enhancements (like supporting multiple registries).

## Requirements

- The existing logic should remain intact
- The provider should use idiomatic go for json processing
- Should have adequate error handling
- Should use the same input/argument signature as the existing Bash solution
- Should have some minimal logging to stdout/stderr

## Tests

We should build a minimal set of both positive and negative tests, using mock input data. We already have mock data.

The Makefile should have a target added to run the tests.

## Build modifications

- The Makefile should be adjusted to also build the binary
- The binary should allow for setting a target arch (and default to the machine current arch)

## Deployment modifications

There should be minor adjustments, if any. The provider binary should be mounted to the same volume etc. and invoked with the same arguments/signature.
