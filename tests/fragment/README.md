# Fragment and ceiling fixtures

Fixtures for `sluisctl policy fragment check` and the schemas under `schemas/policy/tenant/`.
The owner is `acme`. `ceiling-acme.yaml` is its ceiling and `fragment-acme.yaml` stays inside it.
`fragment-five-segments.yaml` and `ceiling-five-segments.yaml` are the same owner with five-segment groups and a project alias.
Each file under `invalid/` is refused, and the first line of the file says by which code or by which schema rule.
The tests in `cmd/sluisctl` and `policy/tenant` read these files.
