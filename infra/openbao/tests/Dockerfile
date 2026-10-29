# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
ARG GO_IMAGE
ARG OPENBAO_IMAGE
ARG JWT_LEGACY_IMAGE
FROM ${JWT_LEGACY_IMAGE} AS legacy-server

FROM ${GO_IMAGE} AS verifier
ARG TARGETARCH
WORKDIR /src
COPY plugins/vault-plugin-secrets-jwt/go.mod plugins/vault-plugin-secrets-jwt/go.sum ./
RUN go mod download
COPY plugins/vault-plugin-secrets-jwt/test/jwtverify/ ./test/jwtverify/
RUN GOOS=linux GOARCH="${TARGETARCH}" CGO_ENABLED=0 \
    go build -trimpath -o /out/jwtverify ./test/jwtverify

FROM ${OPENBAO_IMAGE}
COPY --from=legacy-server --chmod=0555 /usr/bin/bao /test/bao-legacy
COPY --from=verifier --chmod=0555 /out/jwtverify /test/jwtverify
COPY --chmod=0555 tests/runtime-server.sh tests/runtime-bao.sh /test/
COPY scripts/package-jwt-plugins.sh /test/package-jwt-plugins.sh
ENTRYPOINT ["/bin/sleep"]
CMD ["infinity"]
