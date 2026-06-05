# CLI reference

This directory holds the auto-generated per-command markdown emitted by:

```sh
apigw gen-docs --out docs/cli
```

The CI workflow runs this before every release and commits the diff; if
you're looking at this file in a fresh checkout the rest of `docs/cli/`
will be empty until the first `make docs` run.

For interactive help use `apigw <command> --help` — every command ships
with `Examples:` block, persistent flags, and exit-code documentation.
