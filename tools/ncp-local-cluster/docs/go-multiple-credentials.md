# ADR002 Multiple Credentials

## Context

We will need to support multiple credentials for the same registry host. This is not supported out of the box in standard docker config.

## Requirements

- for a single host, be able to extract unique credentials for a repository path

## Examples

nvcr.io/ngc-org/ngc-team/repository
nvcr.io/ngc-org/another-team/another-repository

Each of these repositories has their own credentials.

## Processing

- We may need to modify the format of the injected secret file that contains the actual credentials.
- Our provider config should still work with the match pattern
  - Internally we will handle searching for granular matches on repository
  - We will need a fallback if the path pattern is not found in our injected secret file, propose using the first registry host match

## Input Schema

This maintains the standard dockerconfig json structure, however our implementation will implicitly understand and match on
granular repository paths in each `auths` entry key.

```json
{
  "auths": {
    "nvcr.io/ngc-org/ngc-team/repository": {
      "auth": "base64_auth"
    },
    "nvcr.io/ngc-org/another-team/another-repository": {
      "auth": "base64_auth"
    }
  }
}
```
