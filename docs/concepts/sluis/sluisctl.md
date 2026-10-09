# sluisctl and the GitHub Action

**Status:** built; on laptops since 1.5.5 and inside GitHub Actions jobs since 1.4.0.

## Why a CLI at all

Two reasons used to justify a client binary; one survives. A broker's
client was needed because the identity provider could not exchange a CI
platform's token — that disappears, since the issuer's exchange is a
standard token-endpoint call any `curl` can make. What remains is that
**the cloud CLI has no interactive login**: a person needs something that
runs the browser flow once, exchanges for a role's audience, and answers
as a credential process. kubectl has kubelogin for the same job; the
cloud has nothing. So: one small CLI, with subcommands, because they share
a login cache and an issuer configuration.

## What it is

Subcommands share a login cache and an issuer configuration: `login` once, then `kubeconfig`, `kube-token`,
`aws-config`, `aws`, `token`, `github-token`, `bao`, `psql`/`pg`, `r2`, `exchange`, and `setup` to write the kubeconfig
and AWS profiles in one go. A separate group works on files and not on a sign-in: `render` and `policy render` turn an
estate's installation document into the service and policy documents, deterministically
([0038](../../decisions/0038-estates-render-through-sluis.md)). Every command, flag and exit code is in
[reference/sluisctl.md](../../reference/sluis/sluisctl.md); this page is why it is shaped as it is.

**A job runs the same commands.** With `ACTIONS_ID_TOKEN_REQUEST_URL` and `ACTIONS_ID_TOKEN_REQUEST_TOKEN` set, the
credential commands ask GitHub for the job's own identity token, minted for the issuer, and exchange that, presenting
the audience as the client the way the action does. One committed kubeconfig and one `aws.ini` therefore serve a laptop
and a job alike, where a repository used to keep a second copy of each for CI
([in a job](../../reference/sluis/sluisctl.md#in-a-job)).

## The GitHub Action

One action, at the repository root, for a repository that would rather download nothing of ours into a job. It
exchanges the job's token per audience and prepares exactly what is ours to prepare: a kubeconfig context per
`k8s:<cluster>` audience, a profile `<role>@<account>` per `aws:<account>:<role>` audience (with
`web_identity_token_file` pointing at the exchanged token), and optionally a GitHub App installation token. Inside it
are `curl`, `jq` and two files. Pin a release; there is no floating `v1`. Inputs and outputs are in
[Connect GitHub Actions](../../guides/sluis/connect/github-actions.md#3-use-the-action).

**Everything downstream of an AWS credential is AWS's tooling and runs on top of those profiles**: ECR login,
CodeArtifact tokens, any other service. Neither the action nor the CLI wraps them, on purpose: many registries are just
many profiles and many `--profile` flags, and no version of ours moves when Amazon's tooling does
([recipes](../../guides/sluis/connect/registries-and-artifacts.md)).

<a id="credential-the-broker-for-what-openbao-mints"></a>

## `bao`, `psql` / `pg`, `r2`: couriers for what a store mints

`sluisctl credential ssh|db|client` was removed in v1.34.0
([ADR 0013](../../decisions/0013-openbao-access-through-the-bao-cli.md)): each
reimplemented a slice of what the `bao` CLI already does, and that slice grew
every time OpenBAO did. What replaced it is a courier, and the design is mostly
a list of decisions it does **not** make:

| Decided by | What |
|---|---|
| the exchange client's `requires` | who may ask at all. A session revoked in the console stops issuance within the exchange's token cap, because every run exchanges afresh |
| the OpenBAO role | the lifetime, the extensions, the key id, which principals and names are allowed. No request from here carries a TTL, so a role change reaches every credential in flight |
| `bao` itself | everything after sluisctl's own flags: its subcommands, its flags, its bugs and its fixes stay upstream |
| the caller | which public key is signed, and which principals or names are asked for — a request, never a grant |

**The key is generated for the certificate, not decorated by it.** For `psql`
and `pg` an ECDSA P-384 key is made in the process, a CSR for it goes to
`pki/sign/<role>`, and the key is written beside the certificate that comes
back, in a `0600` file in a `0700` directory — never sent, so a role can offer
`sign` alone and no call exists that would have the manager make a key and put
it on the wire. The certificate is reused only while it has enough life left
and was minted for the same common name.

**What is kept is short-lived and advisory.** The OpenBAO token is cached, one
file per address, login namespace and subject, until a margin before its own
expiry, and `--forget` revokes it and removes it; a login whose answer carries
no lease is used for that one command and never cached. None of it can mint its
own successor but the sign-in's refresh token, which `login` replaces.

**Delivery is where each kind is actually consumed**: `bao` gets `BAO_ADDR`,
`BAO_TOKEN` and, when named, `BAO_CACERT` in its environment alone, and the
process image is replaced where the platform allows it, so an interactive
`bao ssh -mode=ca` gets the terminal as if run directly; `psql` and `pg` get
libpq's own variables. What is printed is what an investigator needs, never the
key. The commands' flags and exit codes are in
[reference/sluisctl.md](../../reference/sluis/sluisctl.md).

## Installing it

Every release carries a Nix flake beside the archives, so a repository whose tools come from devbox names it in
`devbox.json` and `devbox.lock` pins it. How is in [reference/sluisctl.md](../../reference/sluis/sluisctl.md#installing-it).

## What it never does

No stored secrets and no cloud SDK inside. **What is on disk is short-lived and advisory, except the refresh token**,
which a refresh rotates: two commands refreshing at once would sign the operator out of everything, which is why each
credential cache has a lock beside it and why a command with a fresh access token in hand does not spend the refresh
token. None of the caches can mint its own successor; `login` replaces the refresh token. In a job there is no login
cache. Paths and modes are in [reference](../../reference/sluis/sluisctl.md#where-things-are-kept).
