# Service Keys

NGC Service Keys allow programmatic access to NVIDIA Cloud Functions with fine-grained scope control.
Unlike Personal API Keys, which are tied to an individual user, Service Keys are tied to a Nvidia Cloud Account.
This means permissions are not dependent on any individual user's account status. Service Keys let you grant only the specific permissions a workload
requires — for example, an inference service may only need the **Invoke Function** scope while a
deployment pipeline may need **Deploy Function** and **List Functions**.

Service Keys can be created and managed at [org.ngc.nvidia.com/service-keys](https://org.ngc.nvidia.com/service-keys).
For general information on NGC API keys, see the [NGC User Guide](https://docs.nvidia.com/ngc/latest/ngc-user-guide.html#ngc-api-keys).

## Available Scopes

Each Service Key is configured with one or more scopes that determine which Cloud Functions operations are
permitted.

| Scope | Description |
| --- | --- |
| Invoke Function | Allows access to invoke a cloud function. |
| List Functions | Allows access to list cloud functions. |
| List Function Details | Allows access to list details for a cloud function. |
| Queue Details | Allows access to get details of the queues associated with functions. |
| Register Function | Allows access to register a cloud function. |
| Deploy Function | Allows access to deploy a cloud function. |
| Update Function | Allows access to update a cloud function. |
| Delete Function | Allows access to delete a cloud function. |
| Authorize Clients | Allows sharing or removing access to a cloud function. |
| Update Function Secrets | Allows access to update a cloud function's secrets. |
| Manage Registry Credentials | Allows access to manage cloud function registry credentials. |
| Manage Telemetries | Allows access to manage cloud function telemetries. |
| List Clusters | Allows access to list clusters. |
| Read GPU Quota Rule | Allows access to read GPU quota rules. |
| GPU Capacity | Allows access to read GPU availability, capacity, and instance types. |

## Resource Types

Each Service Key is configured with a resource type that controls which entities the key can access.
You select the resource type in the UI when creating the key.

| Resource Type | Description |
| --- | --- |
| All Functions | Grants access to all functions in the account. Supported by most function-related scopes. |
| Function | Grants access to a specific function and all its versions. Use this to restrict a key to a single function (e.g. a key used only to deploy or invoke one particular function). |
| Function Versions | Grants access to specific versions of a function. |
| All Clusters | Grants access to all clusters in the account. Used with cluster management scopes. |
| All Entity | Grants access to all resource types, scoped to your organization. This is the broadest resource type. Some scopes — such as registry credentials, telemetry, GPU quota, and GPU capacity — **only** support this resource type. |

<Info>
The following scopes only work with the **All Entity** resource type. Selecting any other
resource type will result in a 403 error:

- Manage Registry Credentials
- Manage Telemetries
- Read GPU Quota Rule
- GPU Capacity

</Info>

## Scope Requirements

The table below shows which scopes are required for each Cloud Functions CLI action, and which entity types
the key resource must be set to.

<Note>
A Service Key must be configured with both the required scope **and** a compatible resource type. If an
action supports multiple resource types (e.g. All Functions or Function), you can use either to grant
broad or narrowed access respectively.

</Note>

| Action | Required Scope(s) | Supported Entity Types | NGC CLI Example |
| --- | --- | --- | --- |
| Create Function | Register Function | All Functions, All Entity | `ngc cf function create --health-uri /health ...` |
| Deploy Function | Deploy Function, List Functions | All Functions, Function, All Entity | `ngc cf function deploy create --deployment-specification ...` |
| Invoke Function | Invoke Function | All Functions, Function | (invoked via API; no direct CLI equivalent) |
| Get / List Functions | List Functions, List Function Details | All Functions, Function, All Entity | `ngc cf function info <id>:<version>`; `ngc cf function list` |
| Update Function | Update Function | All Functions, Function, All Entity | `ngc cf function update`; `ngc cf update-rate-limit` |
| Delete Function | Delete Function | All Functions, Function, All Entity | `ngc cf function delete` |
| Update Function Secrets | Update Function Secrets | All Functions, Function, All Entity | `ngc cf update-secret` |
| Authorize Clients (share access) | Authorize Clients | All Functions | `ngc cf function authorization add` |
| List Clusters | List Clusters | All Clusters, All Entity | `ngc cf cluster ls` |
| Manage Registry Credentials | Manage Registry Credentials | All Entity only | `ngc cf registry-credential create`; `ngc cf registry-credential list` |
| Manage Telemetry Endpoints | Manage Telemetries | All Entity only | `ngc cf telemetry-endpoint create`; `ngc cf telemetry-endpoint list` |
| GPU Quota | Read GPU Quota Rule | All Entity only | `ngc cf gpu quota` |
| GPU Instance Types / Capacity | GPU Capacity | All Entity only | `ngc cf gpu ls`; `ngc cf gpu capacity` |

## Troubleshooting

### Common 403 Errors

A `403 Forbidden` response when using a Service Key typically means either a required scope is
missing or the resource type is not compatible with the API being called. The following scenarios
cover the most common causes.

**Scope is missing for the action**

> Each API operation requires a specific scope. If your key was not configured with that scope,
> the request will be rejected with a 403. Verify the scope your key has against the
> [Scope Requirements Matrix](./service-keys) and regenerate the key with the
> correct scope if needed.
>
> *Example:* Calling the deploy API with a key that only has the **Register Function** scope
> will fail. The key must also include **Deploy Function**.

**Resource type is incompatible with the scope**

> Each scope only authorizes requests for compatible resource types. Even if the scope is
> correct, using the wrong resource type will cause a 403.
>
> *Example:* A key with **Manage Registry Credentials** and resource type **All Functions** will
> be rejected — this scope requires the **All Entity** resource type. See the important note in
> the [Resource Types](./service-keys) section for the full list of scopes that
> require **All Entity**.

**Key is scoped to a specific Function but the request targets a different function**

> When a key is configured with the **Function** resource type and a specific function ID, it
> can only be used for that function. Requests targeting any other function ID will return a 403.
>
> *Example:* A deploy key scoped to function `abc-123` cannot be used to deploy function
> `xyz-789`. Use the **All Functions** resource type for keys that need to operate across
> multiple functions.

**Deploying a function requires both Deploy Function and List Functions scopes**

> The deploy API requires the **Deploy Function** scope to create or update a deployment, but it
> also calls the function listing API internally to validate the function. A key that has only
> **Deploy Function** without **List Functions** will return a 403.
>
> Ensure keys used for deployment include both **Deploy Function** and **List Functions** scopes.

**Authorize Clients used with a non-All Functions resource type**

> The **Authorize Clients** scope only supports the **All Functions** resource type. Using it
> with **Function**, **Function Versions**, or **All Entity** will result in a 403.

**Recently updated key still returning 403**

> After modifying a Service Key's scopes or resource type, it can take up to **15 minutes** for
> the changes to propagate. Requests made before propagation completes will continue to be
> evaluated against the previous authorization policy and may return a 403.
>
> If you have recently updated a key and are seeing unexpected 403 errors, wait 15 minutes and
> retry before further debugging.
