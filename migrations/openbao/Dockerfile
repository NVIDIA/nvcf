FROM openbao/openbao:2.5.1

# Set build argument for architecture
ARG TARGETARCH
ARG KUBECTL_VERSION

# Install openssl, uuidgen, helm, and kubectl if KUBECTL_VERSION is set to a specific version or latest stable version
RUN apk add --no-cache openssl uuidgen helm curl bash jq && \
    if [ -n "$KUBECTL_VERSION" ]; then \
        curl -Lo /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl"; \
    else \
        curl -Lo /usr/local/bin/kubectl "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/${TARGETARCH}/kubectl"; \
    fi && \
    chmod +x /usr/local/bin/kubectl

WORKDIR /app

COPY --chmod=775 migrations/ /app/migrations/
COPY --chmod=775 addons/ /app/addons/
COPY --chmod=775 entrypoint.sh /app/

CMD ["/app/entrypoint.sh"]
