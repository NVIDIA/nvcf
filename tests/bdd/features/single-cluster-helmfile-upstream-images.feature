@ncp-local @single-cluster @helmfile @upstream-images
Feature: Install a local single-cluster stack with upstream supporting images
  As a self-managed NVCF operator,
  I want the documented upstream image overrides to survive Helmfile rendering
  and installation,
  so that I can use the public supporting images without relying on their NGC
  mirrors.

  Background:
    Given environment variable "NGC_API_KEY" is set
    And environment variable "SAMPLE_NGC_ORG" is set
    And environment variable "SAMPLE_NGC_TEAM" is set
    And environment variable "REPO_ROOT" is set
    And file "tools/ncp-local-cluster/secrets/docker-config.json" exists
    # Conflict precheck: ncp-local-cp claims host ports that overlap with the
    # single-cluster topology. Run
    # `make -C tools/ncp-local-cluster destroy-multicluster` before retrying.
    When I run command "k3d cluster get ncp-local-cp"
    Then the command exit code should be 1
    Given I copy the file "deploy/stacks/self-managed/Makefile.dist" to "deploy/stacks/self-managed/Makefile"
    And I copy the file "tests/bdd/fixtures/self-managed-local-bdd.yaml" to "deploy/stacks/self-managed/environments/local-bdd.yaml"
    And I update yaml file "deploy/stacks/self-managed/environments/local-bdd.yaml" with keys:
      | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
      | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
    And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml"
    # Only ${VAR} is interpolated; bare $oauthtoken stays literal.
    And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
    And I substitute a block in file "deploy/stacks/self-managed/global.yaml.gotmpl":
      """
        reloader:
          image:
            registry: {{ .Values.global.image.registry }}
            repository: {{ .Values.global.image.repository }}/nats-server-config-reloader
            tag: "0.23.0"
      ---
        reloader:
          image:
            registry: docker.io
            repository: natsio/nats-server-config-reloader
            tag: "0.23.0"
      """
    And I substitute a block in file "deploy/stacks/self-managed/global.yaml.gotmpl":
      """
        accountBootstrap:
          image:
            registry: {{ .Values.global.image.registry }}
            repository: {{ .Values.global.image.repository }}/alpine-k8s
            tag: 1.36.1
            pullPolicy: IfNotPresent
      ---
        accountBootstrap:
          image:
            registry: docker.io
            repository: alpine/k8s
            tag: "1.36.1"
            pullPolicy: IfNotPresent
      """
    And a single-cluster ncp-local cluster is running
    And the "nvcr-pull-secret" image pull secret exists in namespaces:
      | cassandra-system |
      | nats-system      |
      | nvcf             |
      | api-keys         |
      | ess              |
      | sis              |
      | vault-system     |
      | nvca-operator    |
      | cert-manager     |

  Scenario: Operator renders and installs the stack with upstream supporting images
    When I run command "env HELM_REGISTRY_CONFIG=${REPO_ROOT}/tools/ncp-local-cluster/secrets/docker-config.json make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd"
    Then the command exit code should be 0
    And the command output should not contain "Error:"

    When I run command:
      """
      rg --fixed-strings 'docker.io/natsio/nats-server-config-reloader:0.23.0' deploy/stacks/self-managed/out -g '**/*-nats/**'
      """
    Then the command exit code should be 0
    And the command output should contain "docker.io/natsio/nats-server-config-reloader:0.23.0"

    # Cassandra initialization currently uses its migrations image. Selecting
    # alpine-k8s for this hook requires chart support.
    When I run command:
      """
      rg --fixed-strings 'nvcf-cassandra-migrations:' deploy/stacks/self-managed/out -g '**/*-cassandra/**'
      """
    Then the command exit code should be 0
    And the command output should contain "nvcf-cassandra-migrations:"

    # The current NATS chart renders NKey Secrets directly and has no nkey job
    # image to override.
    When I run command:
      """
      rg --fixed-strings '# Source: helm-nvcf-nats/templates/nkey-secret.yaml' deploy/stacks/self-managed/out -g '**/*-nats/**'
      """
    Then the command exit code should be 0
    And the command output should contain "nkey-secret.yaml"

    When I run command:
      """
      rg --fixed-strings 'docker.io/alpine/k8s:1.36.1' deploy/stacks/self-managed/out -g '**/*-api/**'
      """
    Then the command exit code should be 0
    And the command output should contain "docker.io/alpine/k8s:1.36.1"

    # Keep this focused on the releases that own or exercise the overrides.
    # A full local stack install also starts unrelated service images that may
    # not support the host architecture.
    When I run command "env HELM_REGISTRY_CONFIG=${REPO_ROOT}/tools/ncp-local-cluster/secrets/docker-config.json make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd HELMFILE_SELECTOR=release-group=dependencies"
    Then the command exit code should be 0

    When I run command "env HELM_REGISTRY_CONFIG=${REPO_ROOT}/tools/ncp-local-cluster/secrets/docker-config.json make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd HELMFILE_SELECTOR=name=ess-api"
    Then the command exit code should be 0

    # The API deployment authenticates to NATS through this runtime service.
    When I run command "env HELM_REGISTRY_CONFIG=${REPO_ROOT}/tools/ncp-local-cluster/secrets/docker-config.json make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd HELMFILE_SELECTOR=name=nats-auth-callout-service"
    Then the command exit code should be 0

    When I run command "env HELM_REGISTRY_CONFIG=${REPO_ROOT}/tools/ncp-local-cluster/secrets/docker-config.json make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd HELMFILE_SELECTOR=name=api"
    Then the command exit code should be 0

    When I run command "helm list --all-namespaces -o json"
    Then the json output should contain rows:
      | name                      | namespace        | status   |
      | nats                      | nats-system      | deployed |
      | cassandra                 | cassandra-system | deployed |
      | ess-api                   | ess              | deployed |
      | nats-auth-callout-service | nats-system      | deployed |
      | api                       | nvcf             | deployed |

    When I run command:
      """
      kubectl get statefulset nats -n nats-system -o 'jsonpath={.spec.template.spec.containers[?(@.name=="reloader")].image}'
      """
    Then the command exit code should be 0
    And the command output should contain "docker.io/natsio/nats-server-config-reloader:0.23.0"
