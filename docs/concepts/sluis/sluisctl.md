# Why is there a CLI and a GitHub Action?

`sluisctl` is one small CLI whose subcommands share a login cache and an issuer configuration. Every command, flag and
exit code is in the [sluisctl reference](../../reference/sluis/sluisctl.md).

## Why a CLI at all?

A CI platform's token needs no client binary, because the issuer's exchange is a standard token-endpoint call any `curl`
can make. The cloud CLI has no interactive login. A person needs something that runs the browser flow once. It then
exchanges for a role's audience and answers as a credential process. kubectl has kubelogin for the same job.

Run `login` once. Then use `kubeconfig`, `kube-token`, `aws-config`, `aws`, `token`, `github-token`, `bao`, `psql`/`pg`,
`r2` or `exchange`. `setup` writes the kubeconfig and AWS profiles in one go.

`render` and `policy render` work on files, not on a sign-in. They turn an installation document into the service and
policy documents deterministically.

## How does a job run the same commands?

With `ACTIONS_ID_TOKEN_REQUEST_URL` and `ACTIONS_ID_TOKEN_REQUEST_TOKEN` set, the credential commands ask GitHub for the
job's own identity token, minted for the issuer, and exchange that. They present the audience as the client, as the
action does. One committed kubeconfig and one `aws.ini` serve a laptop and a job alike
([in a job](../../reference/sluis/sluisctl.md#in-a-job)).

The GitHub Action, at the repository root, is for a repository that downloads nothing of ours into a job. It exchanges
the job's token per audience. It prepares a kubeconfig context per `k8s:<cluster>` audience and a profile
`<role>@<account>` per `aws:<account>:<role>` audience, with `web_identity_token_file` pointing at the exchanged token.
It can also prepare a GitHub App installation token. It uses `curl`, `jq` and two files. Pin a release, because there is no
floating `v1`. Inputs and outputs are in [Connect GitHub Actions](../../guides/sluis/connect/github-actions.md#workflow-side-the-action).

Everything downstream of an AWS credential, such as ECR login or CodeArtifact tokens, is AWS's tooling running on those
profiles. Neither the action nor the CLI wraps it ([recipes](../../guides/sluis/connect/registries-and-artifacts.md)).

<a id="credential-the-broker-for-what-openbao-mints"></a>

## `bao`, `psql` / `pg`, `r2`: couriers for what a store mints

They are couriers for what a store mints. The commands `sluisctl credential ssh|db|client` were removed in v1.34.0. The
courier decides little:

| Decided by | What |
|---|---|
| the exchange client's `requires` | who may ask. A revoked session stops issuance within the exchange's token cap |
| the OpenBao role | the lifetime, extensions, key id, principals and names. No request carries a TTL |
| `bao` itself | everything after sluisctl's own flags |
| the caller | which public key is signed and which principals are asked for: a request, never a grant |

For `psql` and `pg`, an ECDSA P-384 key is made in the process. A CSR goes to `pki/sign/<role>`. The key is written beside
the returned certificate in a `0600` file in a `0700` directory and is never sent. The certificate is reused only while
it has enough life left and has the same common name.

The OpenBao token is cached per address, login namespace and subject until a margin before expiry. `--forget` revokes it
and removes it. A login whose answer carries no lease is used for one command and never cached.

`bao` gets `BAO_ADDR`, `BAO_TOKEN` and, when named, `BAO_CACERT` in its environment alone. The process image is replaced
where the platform allows, so `bao ssh -mode=ca` gets the terminal. `psql` and `pg` get libpq's own variables. Output
never includes the key.

## Installing it

Every release carries a Nix flake beside the archives. A repository whose tools come from devbox names it in
`devbox.json`, and `devbox.lock` pins it. See [installing it](../../reference/sluis/sluisctl.md#installing-it).

## What does it never do?

It holds no stored secrets and has no cloud SDK inside. What is on disk is short-lived and advisory, except the refresh
token. A refresh rotates the token, so two commands refreshing at once would sign the operator out of everything. Each
credential cache has a lock beside it, and a command with a fresh access token does not spend the refresh token.

No cache can mint its own successor. `login` replaces the refresh token. A job has no login cache. Paths and modes are in
[where things are kept](../../reference/sluis/sluisctl.md#where-things-are-kept).

## Decided in

- [ADR 0013](../../decisions/0013-openbao-access-through-the-bao-cli.md): openBao access through the bao CLI
- [ADR 0038](../../decisions/0038-estates-render-through-sluis.md): estates render through sluis
