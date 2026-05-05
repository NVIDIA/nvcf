# NVCF Image Credential Helper Release Process

This project uses [Semantic Versioning](https://semver.org/) with a `v` prefix for git tags.

## Release Tagging

Use `git tag` locally to create a tag for a release, then push it remotely.
Make sure you are on the correct branch for the type of release being performed.

### Major and minor tags

Major/minor tag releases are made from the `main` branch.
Release candidate tags are made prior to cutting a major or minor version release
to ensure all features and fixes for those features are merged to `main`
prior to releasing a production version. They have the format `vX.Y.Z-rc.N`,
where `N` is an integer starting at 0.

```bash
git checkout main
git pull
git tag v1.0.0-rc.0
git push origin tag v1.0.0-rc.0
```

Once a release candidate has been validated by QA, a release tag can be created and pushed
from the last RC tag.

```bash
# Assume rc.2 is the validated tag.
git checkout v1.0.0-rc.2
git tag v1.0.0
git push origin tag v1.0.0
```

### Patch tags

Patch tags *must* be made to a release branch with the pattern `release-X.Y`.

```bash
git checkout release-1.0
git pull
git tag v1.0.1
git push origin tag v1.0.1
```

## CI/CD Pipeline

The CI pipeline automatically handles:

1. Building and testing the code
2. Building and pushing container images
3. Security scanning
4. Release artifact generation

When a tag is pushed, the pipeline will:
- Build the container image with the version tag
- Push to the configured container registries
- Generate release notes
