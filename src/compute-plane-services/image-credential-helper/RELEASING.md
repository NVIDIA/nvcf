# Image credential helper releases

This native component follows the
[monorepo release workflow](../../../docs/dev/github-release-process.md).
Its tag prefix is `src/compute-plane-services/image-credential-helper/v` and
its maintenance branch convention is
`release-src/compute-plane-services/image-credential-helper/vX.Y`.

Release-worthy changes on main use automatic versioning. A scoped maintenance
backport needs an explicit unused patch tag and GitHub Release after review and
validation. Verify the produced image and consuming registry before updating
stack pins. The former standalone bare-tag procedure is retired.
