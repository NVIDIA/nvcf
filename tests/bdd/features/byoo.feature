@byoo @ncp-local @single-cluster @helmfile
Feature: Export workload telemetry to an operator-owned OTLP endpoint
  As a self-managed NVCF tenant,
  I want to register a telemetry endpoint and attach it to my workloads,
  so that the BYOO collector sidecar exports their logs, metrics, and traces.

  # Preconditions beyond the shared local BDD inputs:
  # - The cluster must be able to pull the BYOO collector sidecar image. The
  #   compute-plane stack resolves it as byoo-otel-collector under
  #   global.image.registry and global.image.repository, with the tag pinned
  #   in the stack's base values.
  # - SAMPLE_BYOO_TASK_IMAGE names examples/task-samples/task-byoo-sample and
  #   SAMPLE_BYOO_FUNCTION_IMAGE names
  #   examples/function-samples/byoo-telemetry-sample, each in a registry the
  #   NVCF APIs can validate and the cluster can pull.
  # - The cluster pulls the reference sink image from Docker Hub.
  #
  # TestBYOO runs every scenario. TestBYOOExistingInstall skips @stack-change
  # and runs the workload scenarios against the current local install without
  # changing the stack. Tag every scenario that installs, upgrades, or removes
  # a release with @stack-change.

  Rule: Helmfile installs local NVCF with the BYOObservability gate on

    Background:
      Given these environment variables are set:
        | name            |
        | NGC_API_KEY     |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
        | NVCF_CLI        |
        | REPO_ROOT       |
      # Helmfile pulls OCI charts during installation. Authenticate before sync
      # without exposing the current API key in command arguments or logs.
      And Helm is authenticated to OCI registry "nvcr.io" using the current NGC API key
      And I prepare Helmfile environment "local-bdd-byoo" for stack "self-managed" from fixture "tests/bdd/fixtures/self-managed-local-bdd.yaml" with values:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | addons.llm.enabled              | false                                |
      # The compute observability profile enables the BYOObservability NVCA gate.
      And I prepare Helmfile environment "local-bdd-byoo" for stack "nvcf-compute-plane" from fixture "tests/bdd/fixtures/nvcf-compute-plane-local-bdd.yaml" with values:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | observability.profile           | compute                              |
      And I prepare self-managed secrets file "deploy/stacks/self-managed/secrets/local-bdd-byoo-secrets.yaml" from template "deploy/stacks/self-managed/secrets/secrets.yaml.template" using the current NGC registry credential
      # Conflict precheck: ncp-local-cp's k3d serverlb claims
      # 0.0.0.0:8080/8443/10081, NATS on 4222, and the worker callback port
      # 10086, overlapping host ports single-cluster ncp-local needs. Fail loudly
      # so the operator runs `make -C tools/ncp-local-cluster destroy-multicluster`
      # before retrying. `k3d cluster get` exits 1 when absent (k3d v5).
      And I run command "k3d cluster get ncp-local-cp"
      And the command exit code should be 1
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

    @stack-change
    Scenario: Operator installs NVCF with the BYOObservability gate on
      When I successfully run command "make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd-byoo"

      When I successfully run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --control-plane-stack deploy/stacks/self-managed --env local-bdd-byoo control-plane profile export --cluster-name ncp-local
        """
      Then file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist

      When I successfully run command "${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml init"

      When I successfully run command:
        """
        make -C deploy/stacks/nvcf-compute-plane register-cluster CLUSTER_NAME=ncp-local CONTROL_PLANE_PROFILE=${REPO_ROOT}/deploy/stacks/self-managed/out/control-plane-profile.yaml COMPUTE_KUBE_CONTEXT=k3d-ncp-local NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then file "deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml" should exist

      When I successfully run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd-byoo NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then NVCFBackend "ncp-local" in namespace "nvca-operator" using context "k3d-ncp-local" should report agent status "healthy" within "10m"

  Rule: The BYOO collector sidecar exports workload telemetry to an in-cluster OTLP sink

    # These scenarios depend on the install scenario above in the same feature
    # run, or on an existing local install through TestBYOOExistingInstall.
    Background:
      Given these environment variables are set:
        | name            |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
        | NVCF_CLI        |
        | REPO_ROOT       |
      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"
      # Telemetry registration needs the admin token. This reuses the install
      # scenario's init, or mints a token for an existing install.
      And command has succeeded:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml init
        """
      # NVCA accepts telemetries only with the BYOObservability gate on. The
      # line match keeps an explicit "-BYOObservability" from passing.
      And I successfully run command:
        """
        kubectl --context k3d-ncp-local --namespace nvca-operator get nvcfbackend ncp-local -o 'jsonpath={range .status.featureGate.values[*]}{@}{"\n"}{end}'
        """
      And the command output should have exactly "1" distinct matches of "(?m)^BYOObservability$"
      # Telemetry endpoint names must be unique within an account. A suffix per
      # scenario keeps reruns working if an interrupted run left one behind.
      And I successfully run command "date +%s"
      And I export command output to environment variable "BDD_BYOO_RUN_ID"
      # Reference sinks: stock OpenTelemetry Collector Contrib with an OTLP HTTP
      # receiver, the debug exporter, and its own metrics on port 8888. One sink
      # per workload kind keeps task and function counters apart. NVCA removes
      # NetworkPolicies from workload namespaces on self-managed clusters, so
      # the sidecar reaches the sink without an egress policy.
      #
      # The BYOO collector also forwards its own and scraped metrics, which the
      # receiver counters include. The metrics/task-sample pipeline keeps only
      # metrics whose service.name is byoo-task-test, the resource that
      # examples/task-samples/task-byoo-sample sets, so its exporter counter
      # proves the task's own metrics arrived. The function-sample pipelines
      # do the same for every signal of
      # examples/function-samples/byoo-telemetry-sample, whose service.name is
      # byoo-telemetry-sample.
      And Kubernetes manifest "byoo-otlp-sinks" is:
        """
        apiVersion: v1
        kind: Namespace
        metadata:
          name: bdd-byoo-sink
        ---
        apiVersion: v1
        kind: ConfigMap
        metadata:
          name: otlp-sink
          namespace: bdd-byoo-sink
        data:
          config.yaml: |
            receivers:
              otlp:
                protocols:
                  http:
                    endpoint: 0.0.0.0:4318
            processors:
              filter/task-sample:
                error_mode: ignore
                metrics:
                  metric:
                    - resource.attributes["service.name"] != "byoo-task-test"
              filter/function-sample:
                error_mode: ignore
                logs:
                  log_record:
                    - resource.attributes["service.name"] != "byoo-telemetry-sample"
                metrics:
                  metric:
                    - resource.attributes["service.name"] != "byoo-telemetry-sample"
                traces:
                  span:
                    - resource.attributes["service.name"] != "byoo-telemetry-sample"
            exporters:
              debug:
                verbosity: basic
              debug/task-sample:
                verbosity: basic
              debug/function-sample:
                verbosity: basic
            extensions:
              health_check:
                endpoint: 0.0.0.0:13133
            service:
              extensions: [health_check]
              telemetry:
                metrics:
                  readers:
                    - pull:
                        exporter:
                          prometheus:
                            host: 0.0.0.0
                            port: 8888
              pipelines:
                logs:
                  receivers: [otlp]
                  exporters: [debug]
                metrics:
                  receivers: [otlp]
                  exporters: [debug]
                metrics/task-sample:
                  receivers: [otlp]
                  processors: [filter/task-sample]
                  exporters: [debug/task-sample]
                logs/function-sample:
                  receivers: [otlp]
                  processors: [filter/function-sample]
                  exporters: [debug/function-sample]
                metrics/function-sample:
                  receivers: [otlp]
                  processors: [filter/function-sample]
                  exporters: [debug/function-sample]
                traces/function-sample:
                  receivers: [otlp]
                  processors: [filter/function-sample]
                  exporters: [debug/function-sample]
                traces:
                  receivers: [otlp]
                  exporters: [debug]
        ---
        apiVersion: apps/v1
        kind: Deployment
        metadata:
          name: otlp-sink-task
          namespace: bdd-byoo-sink
        spec:
          replicas: 1
          selector:
            matchLabels:
              app: otlp-sink-task
          template:
            metadata:
              labels:
                app: otlp-sink-task
            spec:
              containers:
                - name: otelcol
                  image: docker.io/otel/opentelemetry-collector-contrib:0.129.1
                  args: ["--config=/etc/otel-sink/config.yaml"]
                  ports:
                    - {name: otlp-http, containerPort: 4318}
                    - {name: metrics, containerPort: 8888}
                  readinessProbe:
                    httpGet: {path: /, port: 13133}
                  resources:
                    requests: {cpu: 50m, memory: 64Mi}
                    limits: {memory: 512Mi}
                  volumeMounts:
                    - {name: config, mountPath: /etc/otel-sink}
              volumes:
                - name: config
                  configMap:
                    name: otlp-sink
        ---
        apiVersion: v1
        kind: Service
        metadata:
          name: otlp-sink-task
          namespace: bdd-byoo-sink
        spec:
          selector:
            app: otlp-sink-task
          ports:
            - {name: otlp-http, port: 4318, targetPort: 4318}
        ---
        apiVersion: apps/v1
        kind: Deployment
        metadata:
          name: otlp-sink-function
          namespace: bdd-byoo-sink
        spec:
          replicas: 1
          selector:
            matchLabels:
              app: otlp-sink-function
          template:
            metadata:
              labels:
                app: otlp-sink-function
            spec:
              containers:
                - name: otelcol
                  image: docker.io/otel/opentelemetry-collector-contrib:0.129.1
                  args: ["--config=/etc/otel-sink/config.yaml"]
                  ports:
                    - {name: otlp-http, containerPort: 4318}
                    - {name: metrics, containerPort: 8888}
                  readinessProbe:
                    httpGet: {path: /, port: 13133}
                  resources:
                    requests: {cpu: 50m, memory: 64Mi}
                    limits: {memory: 512Mi}
                  volumeMounts:
                    - {name: config, mountPath: /etc/otel-sink}
              volumes:
                - name: config
                  configMap:
                    name: otlp-sink
        ---
        apiVersion: v1
        kind: Service
        metadata:
          name: otlp-sink-function
          namespace: bdd-byoo-sink
        spec:
          selector:
            app: otlp-sink-function
          ports:
            - {name: otlp-http, port: 4318, targetPort: 4318}
        """
      And I successfully apply Kubernetes manifest "byoo-otlp-sinks" using contexts:
        | context       |
        | k3d-ncp-local |

    @nvct-task-api
    Scenario: A container task with telemetries exports logs, metrics, and traces
      Given environment variable "SAMPLE_BYOO_TASK_IMAGE" is set
      # A restart resets the sink's counters, so totals below come from this task.
      When I successfully run command "kubectl --context k3d-ncp-local --namespace bdd-byoo-sink rollout restart deployment/otlp-sink-task"
      Then these Kubernetes workloads should complete rollout using context "k3d-ncp-local" within "3m":
        | kind       | name           | namespace     |
        | Deployment | otlp-sink-task | bdd-byoo-sink |
      And Prometheus metrics on port "8888" of pods selected by "app=otlp-sink-task" in namespace "bdd-byoo-sink" using context "k3d-ncp-local" should total within "2m":
        | metric                                      | labels                     | comparison | total |
        | otelcol_receiver_accepted_spans_total       | receiver=otlp              | exactly    | 0     |
        | otelcol_exporter_sent_metric_points_total   | exporter=debug/task-sample | exactly    | 0     |
        | otelcol_receiver_accepted_log_records_total | receiver=otlp              | exactly    | 0     |

      # OTEL_COLLECTOR renders an OTLP HTTP exporter with a bearer header, so
      # the endpoint is the sink's HTTP port and a secret is required.
      Given the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      When I send a "POST" request to "http://api.localhost:8080/v2/nvcf/telemetries" with body:
        """
        {"endpoint":"http://otlp-sink-task.bdd-byoo-sink.svc.cluster.local:4318","protocol":"HTTP","provider":"OTEL_COLLECTOR","types":["LOGS","METRICS","TRACES"],"secret":{"name":"bdd-byoo-task-${BDD_BYOO_RUN_ID}","value":"bdd-sink-token"}}
        """
      Then the HTTP status should be 200
      When I export the HTTP response JSON field "telemetry.telemetryId" to environment variable "BDD_BYOO_TASK_TELEMETRY_ID"
      # Delete the endpoint when the scenario ends, even if it fails.
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      When I send a "DELETE" request to "http://api.localhost:8080/v2/nvcf/telemetries/${BDD_BYOO_TASK_TELEMETRY_ID}" at scenario teardown expecting status 204

      When I successfully generate a task API key with CLI options:
        | option        | value                                |
        | --description | bdd-byoo-task                        |
        | --scopes      | launch_task,task_details,delete_task |

      # Three results one minute apart let the sample's periodic metric export
      # run before the task completes and the collector stops.
      And I successfully create task "bdd-byoo-task" from image "${SAMPLE_BYOO_TASK_IMAGE}" with CLI options:
        | option                 | value                              |
        | --gpu                  | H100                               |
        | --instance-type        | NCP.GPU.H100_1x                    |
        | --backend              | ncp-local                          |
        | --result-strategy      | NONE                               |
        | --max-runtime          | PT10M                              |
        | --max-queued           | PT10M                              |
        | --termination-grace    | PT1M                               |
        | --container-env        | NUM_OF_RESULTS=3                   |
        | --container-env        | DELAY_BETWEEN_RESULTS_IN_MINUTES=1 |
        | --container-env        | FILE_SIZE_BYTES=8192               |
        | --container-env        | INCLUDE_METADATA=false             |
        | --logs-telemetry-id    | ${BDD_BYOO_TASK_TELEMETRY_ID}      |
        | --metrics-telemetry-id | ${BDD_BYOO_TASK_TELEMETRY_ID}      |
        | --traces-telemetry-id  | ${BDD_BYOO_TASK_TELEMETRY_ID}      |

      Then the task selected by NVCF CLI should reach status "COMPLETED" within "900" seconds, polling every "10" seconds
      # Only the task sample's metrics reach the debug/task-sample exporter.
      And Prometheus metrics on port "8888" of pods selected by "app=otlp-sink-task" in namespace "bdd-byoo-sink" using context "k3d-ncp-local" should total within "5m":
        | metric                                      | labels                     | comparison | total |
        | otelcol_receiver_accepted_spans_total       | receiver=otlp              | at least   | 1     |
        | otelcol_exporter_sent_metric_points_total   | exporter=debug/task-sample | at least   | 1     |
        | otelcol_receiver_accepted_log_records_total | receiver=otlp              | at least   | 1     |

      # Delete the task after the sink check so the collector's final export
      # is not cut short.
      And I successfully delete the task selected by NVCF CLI

    @function-lifecycle
    Scenario: A container function with telemetries gets the collector sidecar
      When I successfully run command "kubectl --context k3d-ncp-local --namespace bdd-byoo-sink rollout restart deployment/otlp-sink-function"
      Then these Kubernetes workloads should complete rollout using context "k3d-ncp-local" within "3m":
        | kind       | name               | namespace     |
        | Deployment | otlp-sink-function | bdd-byoo-sink |
      And Prometheus metrics on port "8888" of pods selected by "app=otlp-sink-function" in namespace "bdd-byoo-sink" using context "k3d-ncp-local" should total within "2m":
        | metric                                        | labels        | comparison | total |
        | otelcol_receiver_accepted_metric_points_total | receiver=otlp | exactly    | 0     |

      Given the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      When I send a "POST" request to "http://api.localhost:8080/v2/nvcf/telemetries" with body:
        """
        {"endpoint":"http://otlp-sink-function.bdd-byoo-sink.svc.cluster.local:4318","protocol":"HTTP","provider":"OTEL_COLLECTOR","types":["LOGS","METRICS","TRACES"],"secret":{"name":"bdd-byoo-function-${BDD_BYOO_RUN_ID}","value":"bdd-sink-token"}}
        """
      Then the HTTP status should be 200
      When I export the HTTP response JSON field "telemetry.telemetryId" to environment variable "BDD_BYOO_FUNCTION_TELEMETRY_ID"
      # Queued before the function teardown so it runs after the function is
      # deleted. A telemetry endpoint in use by a function cannot be deleted.
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      When I send a "DELETE" request to "http://api.localhost:8080/v2/nvcf/telemetries/${BDD_BYOO_FUNCTION_TELEMETRY_ID}" at scenario teardown expecting status 204

      When I successfully create function "bdd-byoo-function" from image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8" with CLI options:
        | option                 | value                             |
        | --inference-url        | /echo                             |
        | --inference-port       | 8000                              |
        | --health-uri           | /health                           |
        | --health-port          | 8000                              |
        | --health-timeout       | PT30S                             |
        | --logs-telemetry-id    | ${BDD_BYOO_FUNCTION_TELEMETRY_ID} |
        | --metrics-telemetry-id | ${BDD_BYOO_FUNCTION_TELEMETRY_ID} |
        | --traces-telemetry-id  | ${BDD_BYOO_FUNCTION_TELEMETRY_ID} |
      # Delete the function and undeploy it when the scenario ends, even if the
      # deploy fails. The undeploy runs first and does nothing if nothing deployed.
      And the function selected by NVCF CLI is deleted at scenario teardown
      And the function selected by NVCF CLI is undeployed at scenario teardown
      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value           |
        | --gpu           | H100            |
        | --instance-type | NCP.GPU.H100_1x |
        | --backend       | ncp-local       |
        | --regions       | us-west-1       |
        | --min-instances | 1               |
        | --max-instances | 1               |
        | --timeout       | 900             |
      And I export the function selected by NVCF CLI to environment variables "BDD_BYOO_FUNCTION_ID" and "BDD_BYOO_FUNCTION_VERSION_ID"

      # NVCA adds the collector sidecar to the function's pod.
      When I successfully run command "kubectl --context k3d-ncp-local get pods --all-namespaces --selector function-id=${BDD_BYOO_FUNCTION_ID} -o jsonpath={.items[*].spec.containers[*].name}"
      Then the command output should contain "byoo-otel-collector"

      # load_tester_supreme emits no OTLP. The collector exports its own
      # metrics, which proves the sidecar reaches the registered endpoint.
      Then Prometheus metrics on port "8888" of pods selected by "app=otlp-sink-function" in namespace "bdd-byoo-sink" using context "k3d-ncp-local" should total within "5m":
        | metric                                        | labels        | comparison | total |
        | otelcol_receiver_accepted_metric_points_total | receiver=otlp | at least   | 1     |

    # examples/function-samples/byoo-telemetry-sample answers GET /health and
    # POST /echo on port 8000. Every /echo call exports one span, one log
    # record, and two metric points.
    @function-lifecycle
    Scenario: A BYOO sample function exports logs, metrics, and traces
      Given environment variable "SAMPLE_BYOO_FUNCTION_IMAGE" is set
      When I successfully run command "kubectl --context k3d-ncp-local --namespace bdd-byoo-sink rollout restart deployment/otlp-sink-function"
      Then these Kubernetes workloads should complete rollout using context "k3d-ncp-local" within "3m":
        | kind       | name               | namespace     |
        | Deployment | otlp-sink-function | bdd-byoo-sink |
      And Prometheus metrics on port "8888" of pods selected by "app=otlp-sink-function" in namespace "bdd-byoo-sink" using context "k3d-ncp-local" should total within "2m":
        | metric                                    | labels                         | comparison | total |
        | otelcol_exporter_sent_spans_total         | exporter=debug/function-sample | exactly    | 0     |
        | otelcol_exporter_sent_metric_points_total | exporter=debug/function-sample | exactly    | 0     |
        | otelcol_exporter_sent_log_records_total   | exporter=debug/function-sample | exactly    | 0     |

      Given the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      When I send a "POST" request to "http://api.localhost:8080/v2/nvcf/telemetries" with body:
        """
        {"endpoint":"http://otlp-sink-function.bdd-byoo-sink.svc.cluster.local:4318","protocol":"HTTP","provider":"OTEL_COLLECTOR","types":["LOGS","METRICS","TRACES"],"secret":{"name":"bdd-byoo-sample-${BDD_BYOO_RUN_ID}","value":"bdd-sink-token"}}
        """
      Then the HTTP status should be 200
      When I export the HTTP response JSON field "telemetry.telemetryId" to environment variable "BDD_BYOO_SAMPLE_TELEMETRY_ID"
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      When I send a "DELETE" request to "http://api.localhost:8080/v2/nvcf/telemetries/${BDD_BYOO_SAMPLE_TELEMETRY_ID}" at scenario teardown expecting status 204

      When I successfully create function "bdd-byoo-sample" from image "${SAMPLE_BYOO_FUNCTION_IMAGE}" with CLI options:
        | option                 | value                           |
        | --inference-url        | /echo                           |
        | --inference-port       | 8000                            |
        | --health-uri           | /health                         |
        | --health-port          | 8000                            |
        | --health-timeout       | PT30S                           |
        | --logs-telemetry-id    | ${BDD_BYOO_SAMPLE_TELEMETRY_ID} |
        | --metrics-telemetry-id | ${BDD_BYOO_SAMPLE_TELEMETRY_ID} |
        | --traces-telemetry-id  | ${BDD_BYOO_SAMPLE_TELEMETRY_ID} |
      And the function selected by NVCF CLI is deleted at scenario teardown
      And the function selected by NVCF CLI is undeployed at scenario teardown
      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value           |
        | --gpu           | H100            |
        | --instance-type | NCP.GPU.H100_1x |
        | --backend       | ncp-local       |
        | --regions       | us-west-1       |
        | --min-instances | 1               |
        | --max-instances | 1               |
        | --timeout       | 900             |
      And I successfully generate a function API key with CLI options:
        | option        | value                                                               |
        | --description | bdd-byoo-sample                                                     |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details |
      And I successfully invoke the function selected by NVCF CLI over HTTP with timeout "120" seconds and poll duration "5" seconds:
        """
        {"message":"bdd-byoo-sample"}
        """
      # Only the sample's own signals reach the debug/function-sample exporter.
      Then Prometheus metrics on port "8888" of pods selected by "app=otlp-sink-function" in namespace "bdd-byoo-sink" using context "k3d-ncp-local" should total within "5m":
        | metric                                    | labels                         | comparison | total |
        | otelcol_exporter_sent_spans_total         | exporter=debug/function-sample | at least   | 1     |
        | otelcol_exporter_sent_metric_points_total | exporter=debug/function-sample | at least   | 2     |
        | otelcol_exporter_sent_log_records_total   | exporter=debug/function-sample | at least   | 1     |

  Rule: A workload that asks for BYOO on a cluster without the gate fails with an actionable error

    Background:
      Given these environment variables are set:
        | name            |
        | NGC_API_KEY     |
        | SAMPLE_NGC_ORG  |
        | SAMPLE_NGC_TEAM |
        | NVCF_CLI        |
        | REPO_ROOT       |
      And Helm is authenticated to OCI registry "nvcr.io" using the current NGC API key
      # The local fixture keeps observability.profile disabled, so this
      # environment renders NVCA without the BYOObservability gate.
      And I prepare Helmfile environment "local-bdd-byoo-gate-off" for stack "nvcf-compute-plane" from fixture "tests/bdd/fixtures/nvcf-compute-plane-local-bdd.yaml" with values:
        | global.imagePullSecrets[0].name | nvcr-pull-secret                     |
        | global.helm.sources.repository  | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
        | global.image.repository         | ${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM} |
      And I use NVCF CLI config "${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml"
      And command has succeeded:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml init
        """
      And I successfully run command "date +%s"
      And I export command output to environment variable "BDD_BYOO_RUN_ID"

    # This scenario runs last because it reinstalls NVCA with the gate off;
    # rerun the install scenario to turn the gate back on.
    @function-lifecycle @stack-change
    Scenario: A function with telemetries fails with an actionable error when BYOO is off
      When I successfully run command:
        """
        make -C deploy/stacks/nvcf-compute-plane install CLUSTER_NAME=ncp-local HELMFILE_ENV=local-bdd-byoo-gate-off NVCF_CLI=${NVCF_CLI} NVCF_CLI_CONFIG=${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml
        """
      Then NVCFBackend "ncp-local" in namespace "nvca-operator" using context "k3d-ncp-local" should report agent status "healthy" within "10m"
      When I successfully run command:
        """
        kubectl --context k3d-ncp-local --namespace nvca-operator get nvcfbackend ncp-local -o 'jsonpath={range .status.featureGate.values[*]}{@}{"\n"}{end}'
        """
      Then the command output should have exactly "0" distinct matches of "(?m)^BYOObservability$"

      Given the HTTP request header "Content-Type" is "application/json"
      And the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      When I send a "POST" request to "http://api.localhost:8080/v2/nvcf/telemetries" with body:
        """
        {"endpoint":"http://otlp-sink-function.bdd-byoo-sink.svc.cluster.local:4318","protocol":"HTTP","provider":"OTEL_COLLECTOR","types":["METRICS"],"secret":{"name":"bdd-byoo-gate-off-${BDD_BYOO_RUN_ID}","value":"bdd-sink-token"}}
        """
      Then the HTTP status should be 200
      When I export the HTTP response JSON field "telemetry.telemetryId" to environment variable "BDD_BYOO_GATE_OFF_TELEMETRY_ID"
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      When I send a "DELETE" request to "http://api.localhost:8080/v2/nvcf/telemetries/${BDD_BYOO_GATE_OFF_TELEMETRY_ID}" at scenario teardown expecting status 204

      When I successfully create function "bdd-byoo-gate-off" from image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8" with CLI options:
        | option                 | value                             |
        | --inference-url        | /echo                             |
        | --inference-port       | 8000                              |
        | --health-uri           | /health                           |
        | --health-port          | 8000                              |
        | --health-timeout       | PT30S                             |
        | --metrics-telemetry-id | ${BDD_BYOO_GATE_OFF_TELEMETRY_ID} |
      And the function selected by NVCF CLI is deleted at scenario teardown
      And the function selected by NVCF CLI is undeployed at scenario teardown
      And I export the function selected by NVCF CLI to environment variables "BDD_BYOO_GATE_OFF_FUNCTION_ID" and "BDD_BYOO_GATE_OFF_VERSION_ID"
      # NVCA rejects the creation message and reports the instance as failed,
      # so the deployment moves to ERROR instead of staying in DEPLOYING.
      When I run command:
        """
        ${NVCF_CLI} --config ${REPO_ROOT}/tests/bdd/fixtures/nvcf-cli-local.yaml function deploy create --gpu H100 --instance-type NCP.GPU.H100_1x --backend ncp-local --regions us-west-1 --min-instances 1 --max-instances 1 --timeout 600
        """
      Then the command should fail
      # NVCF copies the reason NVCA reports into the deployment health info.
      Given the HTTP request header "Authorization" is the NVCF CLI admin token with prefix "Bearer "
      When I poll "GET" "http://api.localhost:8080/v2/nvcf/deployments/functions/${BDD_BYOO_GATE_OFF_FUNCTION_ID}/versions/${BDD_BYOO_GATE_OFF_VERSION_ID}" for up to 120 seconds until the HTTP response body contains "Bring Your Own Observability (BYOO) is disabled on this cluster"
      Then the HTTP status should be 200
      And the HTTP response JSON field "deployment.functionStatus" should equal "ERROR"
      And the HTTP response body should contain "the function requests telemetries, but these NVCA feature flags are not enabled: BYOObservability"
