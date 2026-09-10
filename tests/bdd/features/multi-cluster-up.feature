@ncp-local @multi-cluster @env-ncp-local-multicluster-empty
Feature: Bring up a local multi-cluster NVCF stack with the CLI
  As a self-managed NVCF operator,
  I want to install a local multi-cluster NVCF stack,
  so that I can test multi-cluster installation and registration before
  using real infrastructure.

  Rule: self-hosted install installs the control plane; compute-plane CLIs register and install each compute cluster

    Background:
      # SAMPLE_NGC_ORG / SAMPLE_NGC_TEAM are consumed by
      # `make build-and-deploy-multicluster` (the credential provider
      # validation step) when the `multi-cluster ncp-local compute
      # clusters are running` step runs the build target. Without them,
      # that target fails at CREDENTIAL PROVIDER VALIDATION and skips
      # the gateway API setup.
      Given these environment variables are set:
        | name            |
        | NVCF_CLI        |
        | NGC_API_KEY     |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
      # self-hosted install --env local reads the operator-authored
      # control-plane secrets file. Only secrets.yaml.template is tracked.
      # Prepare local-secrets.yaml from that template before running
      # install/register. Ledger snapshots whatever
      # local-secrets.yaml state existed before the first write (its
      # prior contents or absence) and restores or removes it at suite
      # teardown.
      And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
      # --env local also reads operator-authored environment values from
      # both split stacks: deploy/stacks/<stack>/environments/local.yaml.
      # Neither file is tracked, so author both from the BDD multi-cluster
      # fixtures (they carry the alias-service URL shape the split
      # topology needs). observability.profile is disabled because this
      # workflow runs 'helmfile apply', whose diff phase validates
      # rendered manifests against the live cluster (--dry-run=server);
      # on a fresh cluster the ServiceMonitor CRDs do not exist yet and
      # the diff fails before anything installs. The Helmfile workflow
      # (helmfile sync) has no diff phase and keeps the default profile.
      And I prepare Helmfile environment "local" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd-multi.yaml" with values:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | observability.profile           | disabled                             |
      And I prepare Helmfile environment "local" for stack "nvcf-compute-plane" from fixture "tests/bdd/fixtures/nvcf-compute-plane-local-bdd-multi.yaml" with values:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | observability.profile           | disabled                             |
      # Conflict precheck: single-cluster ncp-local's k3d serverlb
      # claims 0.0.0.0:8080/8443/10081, and ncp-local-cp also
      # needs NATS on 4222 plus the worker callback port 10086.
      # Fail loudly here so the operator runs
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
        | cert-manager     |

    Scenario: Operator installs the control plane on a multi-cluster ncp-local stack
      When I run command:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --compute-plane-stack deploy/stacks/nvcf-compute-plane --env local --plain --control-plane-context k3d-ncp-local-cp --compute-plane-context k3d-ncp-local-compute-1 install --control-plane --cluster-name ncp-local-cp --region us-west-1 --nca-id nvcf-default
        """

      Then the command exit code should be 0
      And file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist

    @validate
    Scenario: Control-plane profile describes the multi-cluster control plane
      Given command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --compute-plane-stack deploy/stacks/nvcf-compute-plane --env local --plain --control-plane-context k3d-ncp-local-cp --compute-plane-context k3d-ncp-local-compute-1 install --control-plane --cluster-name ncp-local-cp --region us-west-1 --nca-id nvcf-default
        """

      When I run command:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --compute-plane-stack deploy/stacks/nvcf-compute-plane --env local --plain control-plane profile validate --file deploy/stacks/self-managed/out/control-plane-profile.yaml --require in-cluster
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
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --compute-plane-stack deploy/stacks/nvcf-compute-plane --env local --plain --control-plane-context k3d-ncp-local-cp --compute-plane-context k3d-ncp-local-compute-1 install --control-plane --cluster-name ncp-local-cp --region us-west-1 --nca-id nvcf-default
        """
      And command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml init
        """

      When I run command:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --compute-plane-stack deploy/stacks/nvcf-compute-plane --env local --plain compute-plane register --control-plane-profile deploy/stacks/self-managed/out/control-plane-profile.yaml --cluster-name ncp-local-compute-1 --kube-context k3d-ncp-local-compute-1 --region us-west-1 --output deploy/stacks/nvcf-compute-plane/out/ncp-local-compute-1-register-values.yaml
        """

      Then the command exit code should be 0
      And file "deploy/stacks/nvcf-compute-plane/out/ncp-local-compute-1-register-values.yaml" should exist
      # Subset match (should contain, not should match) because the
      # values file carries non-deterministic IDs alongside the
      # deterministic block. The generated values are asserted as
      # non-empty keys below.
      And yaml file "deploy/stacks/nvcf-compute-plane/out/ncp-local-compute-1-register-values.yaml" should contain:
        """
        clusterName: ncp-local-compute-1
        ncaID: nvcf-default
        region: us-west-1
        selfManaged:
          icmsServiceURL: http://sis.localhost:8080
          revalServiceURL: http://reval.localhost:8080
          natsURL: nats://nats.localhost:4222
        """
      And yaml file "deploy/stacks/nvcf-compute-plane/out/ncp-local-compute-1-register-values.yaml" should have non-empty keys:
        | key                        |
        | clusterID                  |
        | clusterGroupID             |
        | selfManaged.identitySource |

    @nvca-registration
    Scenario: Operator installs the first compute plane
      Given command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --compute-plane-stack deploy/stacks/nvcf-compute-plane --env local --plain --control-plane-context k3d-ncp-local-cp --compute-plane-context k3d-ncp-local-compute-1 install --control-plane --cluster-name ncp-local-cp --region us-west-1 --nca-id nvcf-default
        """
      And command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml init
        """
      And command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --compute-plane-stack deploy/stacks/nvcf-compute-plane --env local --plain compute-plane register --control-plane-profile deploy/stacks/self-managed/out/control-plane-profile.yaml --cluster-name ncp-local-compute-1 --kube-context k3d-ncp-local-compute-1 --region us-west-1 --output deploy/stacks/nvcf-compute-plane/out/ncp-local-compute-1-register-values.yaml
        """

      When I run command:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --compute-plane-stack deploy/stacks/nvcf-compute-plane --env local --plain compute-plane install --values deploy/stacks/nvcf-compute-plane/out/ncp-local-compute-1-register-values.yaml --kube-context k3d-ncp-local-compute-1 --cluster-name ncp-local-compute-1
        """

      Then the command exit code should be 0
