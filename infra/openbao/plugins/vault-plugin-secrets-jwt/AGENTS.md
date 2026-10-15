# vault-plugin-secrets-jwt

Third-party code. This is a modified copy of
[outfoxx/vault-plugin-secrets-jwt](https://github.com/outfoxx/vault-plugin-secrets-jwt),
Apache-2.0, Copyright 2021 Outfox, Inc. Read `NOTICE` before changing anything
here; it enumerates every NVIDIA modification, which Apache-2.0 section 4(b)
requires us to keep accurate.

## Rules

Do not run the repository copyright stamper over this directory. Files that
carry the upstream Outfox header must keep it. Add an NVIDIA header only to a
file NVIDIA authored, and record the change in `NOTICE` in the same commit.

The Go module path is
`github.com/NVIDIA/nvcf/infra/openbao/plugins/vault-plugin-secrets-jwt`, which
deliberately differs from the upstream path. We do not track upstream, so the
path was changed to resolve inside this repository rather than to keep diffs
against Outfox readable.

There is no dependency on `github.com/mariuszs/friendlyid-go` and there must
not be one. That project carries no license and is not redistributable;
`plugin/friendlyid.go` is the independently authored replacement.

## Build and test

    go build ./...
    go test ./...

The image build that consumes this lives in `infra/openbao`.
