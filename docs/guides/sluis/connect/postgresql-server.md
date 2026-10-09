# Configure PostgreSQL for OpenBAO client certificates

Make the server accept certificates that `sluisctl psql` fetches. For the client side, see [Connect PostgreSQL](postgresql.md).

## Before you start

- A certificate works until it expires. PostgreSQL ignores revocation unless `ssl_crl_file` or `ssl_crl_dir` is set.

## Steps

```
# pg_hba.conf: type database user address method
hostssl orders all 10.0.0.0/8 cert map=orders
```

Trust the `issuing_ca` or `ca_chain` the role returns, which is `client-ca.crt`. Use `hostssl ... cert` in `pg_hba.conf`. The `cert` method works only over `hostssl` and verifies the client certificate.

The database role must equal the certificate's `CN`. To map an email or `github-<owner>-<repo>` to a shorter role, keep `map=<name>` and add a `pg_ident.conf` section, and connect with `-U <role>`. See PostgreSQL's [client certificates](https://www.postgresql.org/docs/current/auth-cert.html) and [username maps](https://www.postgresql.org/docs/current/auth-username-maps.html).

For CloudNativePG, a custom client CA replaces the cluster's trust store. Set both fields under `spec.certificates` on the `Cluster`:

- `clientCASecret`: the secret holding the CA's `ca.crt`.
- `replicationTLSSecret`: a `kubernetes.io/tls` secret with a certificate for `streaming_replica`.

These follow the CloudNativePG [certificates documentation](https://cloudnative-pg.io/documentation/1.24/certificates/#client-certificate). Unverified against a live cluster: check the field names against your version.
