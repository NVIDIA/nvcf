# Documentation and inventory tooling

Use the existing inventory parser for new consumers. Retained release inventories
are immutable evidence; validate their source identity without rerendering them.

From the repository root:

```bash
go test -C tools/docs-version-sync ./...
go vet -C tools/docs-version-sync ./...
python3 tools/ci/test-github-release.py
```

Keep commands and release behavior documented in `README.md`. Follow the public
`documentation-style` skill for Markdown and the parent tools guidance for code.
