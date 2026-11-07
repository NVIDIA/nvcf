# Product release metadata

Each `X.Y.Z/` directory contains the umbrella product `manifest.json` and
customer-facing `notes.md`. Follow `docs/dev/umbrella-releases.md` from the
repository root. Released manifests, tags, and assets are immutable.

Select exact released stack tags and original inventories. Do not copy stack
directories from a candidate branch or merge that branch wholesale into main.
The manifest source commit supplies provenance, LICENSE, and NOTICE; it must
be an ancestor of the final publication target on main.

Build the release tool from the repository root:

```sh
go build -C tools/docs-version-sync -o /tmp/nvcf-docs-version-sync .
```

Validate the manifest and selected inventories with `umbrella check`, assemble
with `umbrella bundle`, and verify the result with `umbrella verify-bundle`.
Use `umbrella publish` without `--write` to validate committed metadata and
notes before publication. See the release guide for the required arguments.
Run `git diff --check` before committing. These checks validate packaging;
the release owner records qualification of the selected runtime combination.

Use generated JSON and concise ASCII Markdown. Link customer notes to stable
source commits when the matching maintained documentation is not yet active.
