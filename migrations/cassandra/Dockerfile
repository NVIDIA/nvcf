FROM cassandra:5.0.8

ARG TARGETARCH
ARG KUBECTL_VERSION="1.36.1"
ARG GIT_SHA

USER root

RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get upgrade -y && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates curl gettext-base && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/* && \
    if [ -n "$KUBECTL_VERSION" ]; then \
        curl -Lo /usr/local/bin/kubectl "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl"; \
    else \
        echo "no kubectl version provided, using latest" && \
        curl -Lo /usr/local/bin/kubectl "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/${TARGETARCH}/kubectl"; \
    fi && \
    chmod +x /usr/local/bin/kubectl

# Expects an upstream `golang-migrate` CLI binary for the target
# architecture at files/migrate-${TARGETARCH}. See README "Driver binaries"
# section for the download + rename steps. The migrate database URL in
# execute_sqls.sh uses only upstream-supported parameters, so the stock
# binary works.
ADD --chmod=775 files/migrate-${TARGETARCH} /usr/bin/migrate

COPY keyspaces /app/keyspaces
COPY --chmod=775 execute_sqls.sh /app

LABEL org.opencontainers.image.revision="${GIT_SHA}"

ENTRYPOINT ["/bin/sh"]
CMD ["/app/execute_sqls.sh"]
