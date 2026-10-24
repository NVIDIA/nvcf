@helmfile @cert-manager @upstream-images
Feature: Install cert-manager with explicit upstream images
  As a self-managed operator
  I want my environment image pins to reach the published cert-manager wrapper
  So that I can issue and renew certificates using upstream images

  Background:
    Given these environment variables are set:
      | name             |
      | REPO_ROOT        |
      | KUBECONFIG       |
      | CERT_MANAGER_ENV |
      | CERT_CONTEXT     |
      | CMCTL            |

  Scenario: The published wrapper starts and reconciles certificates with upstream images
    # Prepare an isolated cluster and environment from the authored manifest
    # example before running. This suite never creates or deletes a cluster.
    When I successfully run command:
      """
      env HELMFILE_ENV=${CERT_MANAGER_ENV} helmfile --kube-context ${CERT_CONTEXT} -f ${REPO_ROOT}/deploy/stacks/self-managed/helmfile.d/01-dependencies.yaml.gotmpl --selector name=cert-manager sync
      """
    And I successfully run command "kubectl --context ${CERT_CONTEXT} -n cert-manager rollout status deployment/cert-manager --timeout=180s"
    And I successfully run command "kubectl --context ${CERT_CONTEXT} -n cert-manager rollout status deployment/cert-manager-webhook --timeout=180s"
    And I successfully run command "kubectl --context ${CERT_CONTEXT} -n cert-manager rollout status deployment/cert-manager-cainjector --timeout=180s"
    And I successfully run command "${CMCTL} check api --context ${CERT_CONTEXT} --wait=120s"

    Given Kubernetes manifest "upstream certificate" is:
      """
      apiVersion: cert-manager.io/v1
      kind: Issuer
      metadata:
        name: upstream-test
        namespace: cert-manager
      spec:
        selfSigned: {}
      ---
      apiVersion: cert-manager.io/v1
      kind: Certificate
      metadata:
        name: upstream-test
        namespace: cert-manager
      spec:
        secretName: upstream-test
        commonName: upstream-test.example.com
        isCA: true
        duration: 1h
        renewBefore: 10m
        privateKey:
          algorithm: ECDSA
          size: 256
        issuerRef:
          name: upstream-test
      ---
      apiVersion: admissionregistration.k8s.io/v1
      kind: ValidatingWebhookConfiguration
      metadata:
        name: upstream-ca-injection-test
        annotations:
          cert-manager.io/inject-ca-from: cert-manager/upstream-test
      webhooks:
        - name: upstream-test.example.com
          admissionReviewVersions: [v1]
          sideEffects: None
          clientConfig:
            service:
              name: cert-manager-webhook
              namespace: cert-manager
              path: /validate
          rules: []
      """
    When I successfully apply Kubernetes manifest "upstream certificate" using contexts:
      | context          |
      | ${CERT_CONTEXT}  |
    And I successfully run command "kubectl --context ${CERT_CONTEXT} -n cert-manager wait certificate/upstream-test --for=condition=Ready --timeout=120s"
    And I successfully run command "kubectl --context ${CERT_CONTEXT} -n cert-manager wait certificate/upstream-test --for=jsonpath={.status.revision}=1 --timeout=120s"
    And I successfully run command "kubectl --context ${CERT_CONTEXT} wait validatingwebhookconfiguration/upstream-ca-injection-test --for=jsonpath={.webhooks[0].clientConfig.caBundle} --timeout=120s"
    And I successfully run command:
      """
      /bin/bash -ec 'test "$(kubectl --context ${CERT_CONTEXT} get validatingwebhookconfiguration upstream-ca-injection-test -o jsonpath="{.webhooks[0].clientConfig.caBundle}")" = "$(kubectl --context ${CERT_CONTEXT} -n cert-manager get secret upstream-test -o jsonpath="{.data.ca\.crt}")"'
      """
    And I successfully run command "${CMCTL} renew upstream-test -n cert-manager --context ${CERT_CONTEXT}"
    And I successfully run command "kubectl --context ${CERT_CONTEXT} -n cert-manager wait certificate/upstream-test --for=jsonpath={.status.revision}=2 --timeout=120s"
    And I successfully run command "kubectl --context ${CERT_CONTEXT} -n cert-manager wait certificate/upstream-test --for=condition=Ready --timeout=120s"
