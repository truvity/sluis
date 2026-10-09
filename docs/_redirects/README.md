# Redirects

One YAML file per documentation pass, for example `docs/_redirects/pr3-audit.yaml`. Each maps a moved or merged page to its new home:

```yaml
old/path.md: new/path.md
how-to/retired.md: https://example.com/elsewhere
```

Paths are relative to `docs/`. `hack/mkdocs_hooks.py` merges every file into the redirects plugin, so `mkdocs.yaml` does not change. This directory is excluded from the site and from the word budget.
