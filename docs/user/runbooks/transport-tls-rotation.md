---
---

# Rotate Transport TLS Material

Stargate, Pylon, and `stargate-k8s-router` reload the mounted TLS server
certificate and private key without a process restart. Each service polls its
TLS mount every 30 seconds, so a valid replacement becomes active within about
that long after Kubernetes exposes the complete projected-volume generation.
Certificates are renewed well ahead of expiry, so the outgoing identity stays
valid throughout that window.

The certificate and private key are loaded as one generation. An invalid, empty,
oversized, expired, not-yet-valid, incomplete, or mismatched replacement is
rejected. The process keeps its last-known-good identity and does not enable
insecure mode as a fallback.

A reloaded identity applies to new handshakes. Established connections are not
closed, so an ordinary leaf renewal causes no traffic interruption.

## Scope

Trust bundles do not reload yet. The workload that carries the trust bundle in
practice is Pylon, which dials the request router over the reverse tunnel and
verifies the router server certificate against its configured `--tls-cert-path`.
Changing that bundle still requires restarting Pylon. Plan CA rotation around a
rolling restart of the worker pods until trust reload ships.

Note the asymmetry this creates. `--tls-cert-path` is both the served server
identity and, in the modes that dial, the outbound trust anchor. The served half
now reloads and the dialing half does not, so a Stargate that has picked up a
rotated identity, reported a successful reload and stayed ready is still dialing
direct-registered backends with the trust bytes it read at startup. A renewal
signed by an already-trusted root is unaffected. A CA change is not: restart the
pod rather than waiting for it to converge.

Stargate's relay trust has the same shape but cannot be reached in a supported
deployment, since relaying requires `--enable-dev-peer-forwarding`, which is
development-only. `stargate-k8s-router` already takes its WebTransport upstream
trust from a separate `--upstream-tls-cert-path`.

## Point Pylon at the root CA, not an intermediate

Configure Pylon's trust bundle with the root CA certificate. Do not pin an
intermediate.

An intermediate is renewed far more often than a root. If Pylon trusts an
intermediate directly, every intermediate renewal means a new trust bundle for
Pylon, and because trust does not reload yet, each one costs a rolling restart
of every Pylon pod. Those pods hold GPU workloads, so that restart is the
expensive half of the rotation.

If Pylon trusts the root instead, it keeps validating through renewed
intermediates and leaves with no trust-bundle change and no restart. The router
server certificate reloads in place, so the entire renewal path stays
restart-free. Serve the full chain from the router, leaf first and intermediates
after, so Pylon can build the path back to the root. Reserve trust-bundle
changes, and the Pylon restart that comes with them, for an actual root
rotation.

The same reasoning applies to the other trust consumers, Stargate dialing direct
backends and `stargate-k8s-router` dialing upstream endpoints, but Pylon is the
one where the restart is worth designing around.

## Renew a server certificate

1. Publish the replacement certificate and private key as one atomic Kubernetes
   Secret update. Keep both in the same Secret so the projected `..data` symlink
   exposes one complete generation.
2. Wait for each component to report a successful `server_identity` reload.
3. Verify new connections through Stargate, Pylon, and `stargate-k8s-router`.

No pod restart is required, and no workload restart is needed for a renewal
signed by a CA that every peer already trusts.

## Rotate a CA

This is only needed for a root rotation. If Pylon trusts the root, renewing an
intermediate needs no trust-bundle change and no restart. Because trust does not
reload yet, use this order for a root rotation:

1. Add the new root CA certificate to Pylon's trust bundle, keeping the old
   root.
2. Roll the Pylon pods so they pick the new bundle up.
3. Replace each server certificate and private key with an identity chaining to
   the new root. This step reloads in place and needs no restart.
4. Verify new connections.
5. Remove the old root from Pylon's trust bundle and roll the Pylon pods again.

For emergency revocation of a root, restart Pylon rather than waiting for a
reload. Removing a trust root has no effect on a running process.

## Mount the Secret as a directory, not with subPath

Reload depends on the projected-volume layout. Kubernetes exposes a mounted
Secret as `<mount>/..data`, a symlink to the current generation directory, with
`tls.crt` and `tls.key` symlinked through it. Rotation swaps that one symlink
atomically, which is what lets a poll read one complete generation rather than a
torn pair.

A `subPath` volume mount does not get that layout. Kubernetes never propagates
Secret updates into a `subPath` mount, so the file contents never change. Polling
cannot help, because there is nothing on disk to notice: reload stays permanently
inert while appearing configured.

Mount the whole Secret at a directory and point `tls.certPath` and `tls.keyPath`
inside it. The `llm-request-router` chart already does this and rejects a
certificate and key in different directories.

## Verify a rotation

Confirm the mount is projected and read the current generation:

```bash
kubectl exec -n <namespace> <pod> -- readlink -f <tls-mount>/<tls.certPath>
kubectl exec -n <namespace> <pod> -- readlink -f <tls-mount>/<tls.keyPath>
```

Both must resolve through `..data` into the same generation directory. A path
that resolves to itself is not a projected mount and will never rotate.

Check reload counters and the active server certificate expiry:

```bash
curl -fsS http://<metrics-endpoint>/metrics | grep tls_reloads_total
curl -fsS http://<metrics-endpoint>/metrics | grep tls_certificate_expiry_seconds
```

The counters use only `material_type` and `result` labels. The expected material
type is `server_identity`. Expected results are `success` and `rejected`.

A component that serves a generated self-signed identity has no expiry to
report and publishes no `tls_certificate_expiry_seconds` series at all. Where
the series is present, the value is the real `notAfter` of the active
certificate, so `tls_certificate_expiry_seconds - time()` is the remaining
lifetime. An expiry that stops moving across renewals is the signal that reload
is inert, most often a `subPath` mount.

Check component readiness after a rotation:

```bash
curl -fsS http://<health-endpoint>/readyz
```

An active server identity that expires before a valid replacement is installed
makes Stargate and `stargate-k8s-router` not ready, which removes the pod from
its Service endpoints. Pylon has no readiness probe; check its reload counters
and expiry gauge instead.

## Recover from a rejected reload

1. Check the component log for `TLS material reload rejected`.
2. Confirm that the certificate and private key are non-empty PEM files.
3. Confirm that the certificate and key match and that every certificate in the
   served chain is currently valid.
4. Publish a complete corrected Secret generation.
5. Wait for a successful reload counter increment.
6. Test a new connection with the expected CA and server name.

A rejected later generation does not interrupt the last-known-good identity. The
same rejection is reported once rather than on every filesystem event, so a
stuck generation does not flood the log or the rejection counter. If no valid
material can be loaded during startup, the process fails startup instead of
weakening TLS.
