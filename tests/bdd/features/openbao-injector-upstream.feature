@helmfile @openbao @upstream-images
Feature: Deliver secrets with an upstream admission injector
  As a self-managed operator
  I want the published OpenBao wrapper to honor my immutable injector image
  So that injected workloads can retrieve and renew credentials

  Background:
    Given these environment variables are set:
      | name                    |
      | KUBECONFIG              |
      | INJECTOR_CONTEXT        |
      | INJECTOR_STACK_DIR      |
      | INJECTOR_ENV            |
      | INJECTOR_EXPECTED_IMAGE |
      | INJECTOR_AGENT_IMAGE    |
      | INJECTOR_PULL_SECRET    |

  Scenario: Upstream injector mutates a workload that retrieves a secret and renews its token
    # Prepare the isolated backend, short-period auth role and fixture secret
    # as described in injector-upstream.md. No cluster lifecycle is hidden here.
    When I successfully run command:
      """
      env HELMFILE_ENV=${INJECTOR_ENV} helmfile --kube-context ${INJECTOR_CONTEXT} -f ${INJECTOR_STACK_DIR}/helmfile.d/01-dependencies.yaml.gotmpl --selector name=openbao-server sync --skip-needs --args --no-hooks --wait --timeout 180
      """
    Then deployment "openbao-server-agent-injector" in namespace "vault-system" using context "${INJECTOR_CONTEXT}" should complete rollout within "180s"
    When I run command "kubectl --context ${INJECTOR_CONTEXT} -n vault-system get deployment openbao-server-agent-injector -o=jsonpath={.spec.template.spec.containers[0].image}"
    Then the command exit code should be 0
    And the command output should contain "${INJECTOR_EXPECTED_IMAGE}"
    When I successfully run command:
      """
      kubectl --context ${INJECTOR_CONTEXT} -n vault-system exec deployment/openbao-server-agent-injector -c sidecar-injector -- /bin/sh -ec 'test "$(id -u)" = 100; test "$(id -g)" = 1000'
      """

    # This maintenance runner uses the existing raw-command step for manifests.
    When I successfully run command:
      """
      /bin/bash -ec 'kubectl --context ${INJECTOR_CONTEXT} apply -f - <<\MANIFEST
      apiVersion: v1
      kind: Pod
      metadata:
        name: injected-consumer
        namespace: injector-workload
        annotations:
          vault.hashicorp.com/agent-inject: "true"
          vault.hashicorp.com/role: injector-qa
          vault.hashicorp.com/agent-inject-secret-message: qa/data/injector
          vault.hashicorp.com/agent-inject-template-message: |
            {{- with secret "qa/data/injector" -}}{{ .Data.data.message }}{{- end -}}
          vault.hashicorp.com/agent-inject-token: "true"
          vault.hashicorp.com/agent-run-as-user: "100"
          vault.hashicorp.com/agent-run-as-group: "1000"
      spec:
        serviceAccountName: injector-qa
        imagePullSecrets:
          - name: ${INJECTOR_PULL_SECRET}
        securityContext:
          runAsNonRoot: true
          runAsUser: 100
          runAsGroup: 1000
          fsGroup: 1000
          seccompProfile:
            type: RuntimeDefault
        containers:
          - name: consumer
            image: ${INJECTOR_AGENT_IMAGE}
            command: [/bin/sh, -ec, "while true; do sleep 30; done"]
            securityContext:
              allowPrivilegeEscalation: false
              capabilities:
                drop: [ALL]
            resources:
              requests:
                cpu: 10m
                memory: 32Mi
              limits:
                cpu: 100m
                memory: 128Mi
      MANIFEST'
      """
    And I successfully run command "kubectl --context ${INJECTOR_CONTEXT} -n injector-workload wait pod/injected-consumer --for=condition=Ready --timeout=180s"
    When I run command "kubectl --context ${INJECTOR_CONTEXT} -n injector-workload get pod injected-consumer -o=jsonpath={.spec.initContainers[0].image}"
    Then the command exit code should be 0
    And the command output should contain "${INJECTOR_AGENT_IMAGE}"
    When I run command:
      """
      kubectl --context ${INJECTOR_CONTEXT} -n injector-workload get pod injected-consumer -o 'jsonpath={.spec.containers[?(@.name=="vault-agent")].image}'
      """
    Then the command exit code should be 0
    And the command output should contain "${INJECTOR_AGENT_IMAGE}"
    When I successfully run command:
      """
      kubectl --context ${INJECTOR_CONTEXT} -n injector-workload exec injected-consumer -c consumer -- /bin/sh -ec 'test "$(id -u)" = 100; test "$(id -g)" = 1000; test "$(cat /vault/secrets/message)" = upstream-injector-secret-ok'
      """
    And I successfully run command:
      """
      kubectl --context ${INJECTOR_CONTEXT} -n injector-workload exec injected-consumer -c vault-agent -- /bin/sh -ec 'test "$(id -u)" = 100; test "$(id -g)" = 1000'
      """
    # Read the token only inside the pod; never put it in argv or runner logs.
    And I successfully run command:
      """
      kubectl --context ${INJECTOR_CONTEXT} -n injector-workload exec injected-consumer -c consumer -- /bin/sh -ec 'export BAO_ADDR=http://openbao-server.vault-system.svc.cluster.local:8200; export BAO_TOKEN=$(cat /vault/secrets/token); created=$(bao token lookup -format=json | jq -r ".data.creation_time"); sleep 45; bao token lookup -format=json | jq -e --argjson created "$created" ".data | .renewable and (.period == 30) and (.ttl > 0) and (.creation_time == \$created) and (.last_renewal_time > \$created)"'
      """
