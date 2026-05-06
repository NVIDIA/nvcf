# minijinja

This repository is a customization wrapper around [mitsuhiko/minijinja][mj].
Running `scripts/build.sh` will pull minijinja, apply `etc/patch.diff` (our
customizations), build the `minijinja-cabi` crate, and install the resulting
libraries to `lib/`.

We target the following platforms:

- aarch64-apple-darwin
- x86_64-apple-darwin
- aarch64-unknown-linux-musl
- x86_64-unknown-linux-musl

Linux libraries are built statically as the `-Wl,--allow-multiple-definition`
LDFLAGS param permits linking against multiple Rust crates (this one, and
tokenizer). Darwin binaries, however, are built dynamically as there is no
ability to allow multiple symbol definitions; consequently, they are also
modified to use rpath after building so that CGo can inject the rpath that they
should be loaded from.

## Tooling

This repository uses [Mise][mise] for tooling and [Hk][hk] as an experiment.
Just ensure that `mise` is available in `$PATH` and it should manage itself.
Some helpful commands include:

- `mise run test`: run all tests
- `mise run check`: lint without fixes
- `mise run fix`: lint with fixes

[mj]: https://github.com/mitsuhiko/minijinja
[mise]: https://mise.jdx.dev/
[hk]: https://hk.jdx.dev/
