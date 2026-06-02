@ncp-local @multi-cluster @env-ncp-local-multicluster-empty
Feature: Bring up a local multi-cluster NVCF stack with the CLI
  As a self-managed NVCF operator,
  I want to install a local multi-cluster NVCF stack,
  so that I can test multi-cluster installation and registration before
  using real infrastructure.

  Rule: self-hosted install installs the control plane; compute-plane CLIs register and install each compute cluster

    Background:
      Given environment variable "NVCF_CLI" is set
      And environment variable "NGC_API_KEY" is set
      # SAMPLE_NGC_ORG / SAMPLE_NGC_TEAM are consumed by
      # `make build-and-deploy-multicluster` (the credential provider
      # validation step) when the `multi-cluster ncp-local compute
      # clusters are running` step runs the build target. Without them,
      # that target fails at CREDENTIAL PROVIDER VALIDATION and skips
      # the gateway API setup.
      And environment variable "SAMPLE_NGC_ORG" is set
      And environment variable "SAMPLE_NGC_TEAM" is set
      # self-hosted install --env local pulls credentials from
      # deploy/stacks/self-managed/secrets/local-secrets.yaml, which is
      # gitignored and operator-authored (only secrets.yaml.template is
      # tracked). Author the file from the canonical template the same
      # way the Helmfile feature authors local-bdd-secrets.yaml: copy
      # the template into place, then substitute the placeholder with
      # the base64 NGC dockerconfig credential. Ledger snapshots
      # whatever local-secrets.yaml state existed before the first
      # write (its prior contents or absence) and restores or removes
      # it at suite teardown.
      And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-secrets.yaml"
      And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
      # Conflict precheck: single-cluster ncp-local's k3d serverlb
      # claims 0.0.0.0:8080/8443/4222, the same host ports
      # ncp-local-cp needs in the multi-cluster topology. Fail
      # loudly here so the operator runs
      # `make -C tools/ncp-local-cluster destroy CLUSTER_NAME=ncp-local`
      # before retrying this feature, rather than discovering the
      # port collision deep inside build-and-deploy-multicluster.
      # `k3d cluster get` exits 1 when the named cluster is absent
      # (k3d v5).
      And I run command "k3d cluster get ncp-local"
      And the command exit code should be 1
      And multi-cluster ncp-local compute clusters are running:
        | ncp-local-compute-1 |
      # nvcf-cli self-hosted install renders helmfile manifests that
      # reference imagePullSecrets: [{name: nvcr-pull-secret}]. Create
      # the secret in each NVCF namespace before install so pods can
      # pull nvcr.io images. The step is idempotent (kubectl apply).
      # Real users were running this loop manually before reaching the
      # install command.
      And the "nvcr-pull-secret" image pull secret exists in namespaces:
        | cassandra-system |
        | nats-system      |
        | nvcf             |
        | api-keys         |
        | ess              |
        | sis              |
        | vault-system     |
        | nvca-operator    |
        | nvca-system      |
        | nvcf-backend     |
        | cert-manager     |

    Scenario: Operator installs the control plane on a multi-cluster ncp-local stack
      When I run command:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain --control-plane-context k3d-ncp-local-cp --compute-plane-context k3d-ncp-local-compute-1 install --control-plane --cluster-name ncp-local-cp --region us-west-1 --nca-id nvcf-default
        """

      Then the command exit code should be 0
      And file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist

    @validate
    Scenario: Control-plane profile describes the multi-cluster control plane
      Given command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain --control-plane-context k3d-ncp-local-cp --compute-plane-context k3d-ncp-local-compute-1 install --control-plane --cluster-name ncp-local-cp --region us-west-1 --nca-id nvcf-default
        """

      When I run command:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain control-plane profile validate --file deploy/stacks/self-managed/out/control-plane-profile.yaml --require in-cluster
        """
      Then the command exit code should be 0

      # Subset match (should contain, not should match) so additive
      # changes to the profile schema do not break this scenario.
      # Tighten to should match if extra-field drift becomes a bug source.
      And yaml file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should contain:
        """
        apiVersion: nvcf.nvidia.com/v1alpha1
        kind: ControlPlaneProfile
        controlPlane:
          clusterName: ncp-local-cp
          ncaID: nvcf-default
          region: us-west-1
          endpoints:
            inCluster:
              icmsURL: http://api.sis.svc.cluster.local:8080
              revalURL: http://reval.nvcf.svc.cluster.local:8080
              natsURL: nats://nats.nats-system.svc.cluster.local:4222
            computeReachable:
              icmsURL: http://sis.localhost:8080
              revalURL: http://reval.localhost:8080
              natsURL: nats://nats.localhost:4222
          gateway:
            httpURL: http://api.localhost:8080
            grpcURL: grpc.localhost:10081
          hosts:
            api: api.localhost
            apiKeys: api-keys.localhost
            sis: sis.localhost
            reval: reval.localhost
            nats: nats.localhost
            invocation: invocation.localhost
        """

    @nvca-registration
    Scenario: Operator registers the first compute plane and validates its handoff artifact
      Given command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain --control-plane-context k3d-ncp-local-cp --compute-plane-context k3d-ncp-local-compute-1 install --control-plane --cluster-name ncp-local-cp --region us-west-1 --nca-id nvcf-default
        """
      And command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml init
        """

      When I run command:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain compute-plane register --control-plane-profile deploy/stacks/self-managed/out/control-plane-profile.yaml --cluster-name ncp-local-compute-1 --kube-context k3d-ncp-local-compute-1 --region us-west-1 --output deploy/stacks/self-managed/out/ncp-local-compute-1-register-values.yaml
        """

      Then the command exit code should be 0
      And file "deploy/stacks/self-managed/out/ncp-local-compute-1-register-values.yaml" should exist
      # Subset match (should contain, not should match) because the
      # values file carries non-deterministic IDs alongside the
      # deterministic block. The IDs are asserted by individual
      # should-not-be-empty steps below.
      And yaml file "deploy/stacks/self-managed/out/ncp-local-compute-1-register-values.yaml" should contain:
        """
        clusterName: ncp-local-compute-1
        ncaID: nvcf-default
        region: us-west-1
        selfManaged:
          icmsServiceURL: http://sis.localhost:8080
          revalServiceURL: http://reval.localhost:8080
          natsURL: nats://nats.localhost:4222
        """
      And yaml file "deploy/stacks/self-managed/out/ncp-local-compute-1-register-values.yaml" key "clusterID" should not be empty
      And yaml file "deploy/stacks/self-managed/out/ncp-local-compute-1-register-values.yaml" key "clusterGroupID" should not be empty
      And yaml file "deploy/stacks/self-managed/out/ncp-local-compute-1-register-values.yaml" key "selfManaged.identitySource" should not be empty

    @nvca-registration
    Scenario: Operator installs the first compute plane
      Given command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain --control-plane-context k3d-ncp-local-cp --compute-plane-context k3d-ncp-local-compute-1 install --control-plane --cluster-name ncp-local-cp --region us-west-1 --nca-id nvcf-default
        """
      And command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml init
        """
      And command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain compute-plane register --control-plane-profile deploy/stacks/self-managed/out/control-plane-profile.yaml --cluster-name ncp-local-compute-1 --kube-context k3d-ncp-local-compute-1 --region us-west-1 --output deploy/stacks/self-managed/out/ncp-local-compute-1-register-values.yaml
        """

      When I run command:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain compute-plane install --values deploy/stacks/self-managed/out/ncp-local-compute-1-register-values.yaml --kube-context k3d-ncp-local-compute-1 --cluster-name ncp-local-compute-1
        """

      Then the command exit code should be 0
