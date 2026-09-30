# NVCF self-managed stack 0.6 maintenance

This subtree contains the 0.6 stack composition for patch releases. It restores
the 0.6.1 distribution and retains the injector webhook override support added
on its maintenance line. Charts and images are pinned independently of the
current monorepo service sources.

## Deployment

Use Helm 3 and Helmfile 1.1.x. From this directory, create
`environments/customer.yaml` with your chart registry, image registry, storage,
and ingress settings. Start from the defaults in `environments/base.yaml`.

```sh
cp secrets/secrets.yaml.template secrets/customer-secrets.yaml
# Fill in the registry credential and deployment secrets.
make template HELMFILE_ENV=customer
make install HELMFILE_ENV=customer
```

Review generated manifests in `out/` before installation. The secrets file and
generated manifests are local deployment inputs.

## Releases

`VERSION` identifies the candidate stack version. The release helper can cut
an `X.Y.Z-rc.N` from a reviewed candidate branch with the `release-tags`
workflow's `release-candidate` operation and service
`nvcf-self-managed-stack`.

Stable 0.6 patches publish from
`release-deploy/stacks/self-managed/v0.6`. Pushing a stack change to that branch
creates the next stable tag, so activate it only after review and upgrade QA.
Tags use `deploy/stacks/self-managed/vX.Y.Z` and identify the immutable stack
source consumed by the artifact publisher.

`release-inventory.yaml` lists this distribution's three Helmfile states for
release inventory generation.
