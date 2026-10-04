## What and why

<!-- One or two sentences. Link the issue or discussion: "Closes #N" only if
this PR really ends it, otherwise "Refs #N". -->

## Checklist

See [CONTRIBUTING.md](../CONTRIBUTING.md#pr-checklist) for the details.

- [ ] Commit subjects follow Conventional Commits, and a `feat`/`fix`/`perf`
      summary reads as a plain end-user statement (it goes into the release
      notes almost verbatim).
- [ ] `go vet ./...` and `go test ./...` pass, `make build-arm` cross-compiles,
      and the code is `gofmt`-clean.
- [ ] Agent changes: tested on real hardware, or noted here that they were not.
- [ ] New UI strings are in every `i18n/bundles/` locale.
- [ ] No personal data (real LAN IPs, MAC addresses, serials, e-mail
      addresses) and no Bose firmware, dumps or other Bose material.
- [ ] At least one label is set.
