#!/usr/bin/env bash
#
# Create the four conformance plans against a running suite, and print
# their ids -- one for the Config profile and three for
# hack/conformance_drive.py to walk.
#
# The client secrets are read at the moment of use, go into a temporary
# directory that is removed on exit, and are never printed: a plan
# configuration is the one place they have to be written down, and it
# lives inside the suite, which is why the suite's control plane is not
# public (docs/guides/sluis/run-conformance.md).
#
# Usage:
#   hack/conformance-plans.sh [<version label>]
#
#   ISSUER     the issuer to certify, e.g. https://access.example.com (required)
#   LABEL      the first argument; goes into the plans' descriptions
#   ALIAS      the path segment the clients register, /test/a/<alias>/callback
#              (default access-issuer)
#
# Where the suite is:
#   SUITE      the control-plane URL, e.g. http://10.0.0.7:8080. Unset, the
#              ClusterIP of Service conformance-suite in namespace conformance,
#              read with KUBECTL.
#   KUBECTL    how the cluster is reached (a context that authenticates
#              through OIDC needs kubectl-oidc_login on PATH)
#
# Where the client secrets are (the four clients conformance, conformance-2,
# conformance-bc and conformance-bc-2):
#   SECRETS=kubernetes   (default) Secret <id>-client, key client-secret, in
#                        namespace CLIENT_NAMESPACE (default access-issuer)
#   SECRETS=ssm          the oidc/v1 document external/oidc/<id> of an
#                        installation on AWS Lambda, read with the aws CLI
#                        under the SSM root /sluis/$INSTANCE. The caller's
#                        credentials (AWS_PROFILE, AWS_REGION) must be allowed
#                        to decrypt it; the issuer's own role is not needed.
set -euo pipefail
K="${KUBECTL:-kubectl}"
LABEL="${1:-${LABEL:-$(git -C "$(dirname "$0")/.." describe --tags --always 2>/dev/null || echo dev)}}"
if [ -n "${SUITE:-}" ]; then
  S="$SUITE"
else
  IP=$($K -n conformance get svc conformance-suite -o jsonpath='{.spec.clusterIP}')
  S="http://$IP:8080"
fi
H='X-Forwarded-Proto: https'
echo "SUITE=$S"
curl -sf -H "$H" -o /dev/null "$S/api/runner/available" || { echo "suite not answering at $S"; exit 1; }
DIR=$(mktemp -d); trap 'rm -rf "$DIR"' EXIT
SECRETS="${SECRETS:-kubernetes}"
case "$SECRETS" in
  kubernetes)
    CLIENT_NAMESPACE="${CLIENT_NAMESPACE:-access-issuer}"
    secret() { $K -n "$CLIENT_NAMESPACE" get secret "$1-client" -o jsonpath='{.data.client-secret}' | base64 -d; } ;;
  ssm)
    : "${INSTANCE:?set INSTANCE to the instance name, the SSM root is /sluis/INSTANCE}"
    secret() {
      aws ssm get-parameter --with-decryption --name "/sluis/$INSTANCE/external/oidc/$1" \
        --query Parameter.Value --output text | jq -r '."client-secret"'
    } ;;
  *) echo "SECRETS must be kubernetes or ssm" >&2; exit 2 ;;
esac
# Two pairs of clients (declared in the installation's policy): `conformance` and
# `conformance-2` register NO back-channel address and serve Basic OP and
# RP-Initiated Logout, whose modules fail a test that receives a logout
# token they did not expect; `conformance-bc` and `conformance-bc-2`
# register one and serve the Back-Channel plan, which expects it.
S1=$(secret conformance);    S2=$(secret conformance-2)
B1=$(secret conformance-bc); B2=$(secret conformance-bc-2)
ISS="${ISSUER:?set ISSUER to the issuer to certify, e.g. https://access.example.com}/.well-known/openid-configuration"
ALIAS="${ALIAS:-access-issuer}" S1="$S1" S2="$S2" B1="$B1" B2="$B2" python3 - "$DIR" "$ISS" "$LABEL" <<'PY'
import json, os, sys, pathlib
d, iss, label = sys.argv[1:]
s1, s2, b1, b2 = (os.environ[k] for k in ("S1", "S2", "B1", "B2"))
alias = os.environ["ALIAS"]
common = {"server": {"discoveryUrl": iss}}
pathlib.Path(d, "config.json").write_text(json.dumps({**common, "alias": alias + "-config",
  "description": alias + " " + label + " - Config", "client": {"client_id": "conformance", "client_secret": "unused-here"}}))
pathlib.Path(d, "basic.json").write_text(json.dumps({**common, "alias": alias,
  "description": alias + " " + label,
  "client": {"client_id": "conformance", "client_secret": s1},
  "client2": {"client_id": "conformance-2", "client_secret": s2},
  "client_secret_post": {"client_id": "conformance", "client_secret": s1}}))
pathlib.Path(d, "backchannel.json").write_text(json.dumps({**common, "alias": alias,
  "description": alias + " " + label + " - Back-Channel",
  "client": {"client_id": "conformance-bc", "client_secret": b1},
  "client2": {"client_id": "conformance-bc-2", "client_secret": b2},
  "client_secret_post": {"client_id": "conformance-bc", "client_secret": b1}}))
PY
mk() { curl -s -H "$H" -X POST "$S/api/plan?planName=$1&variant=$2" -H 'Content-Type: application/json' --data-binary @"$DIR/$3" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("id") or d)'; }
BASIC='%7B%22server_metadata%22%3A%22discovery%22%2C%22client_registration%22%3A%22static_client%22%7D'
LOGOUT='%7B%22client_registration%22%3A%22static_client%22%2C%22response_type%22%3A%22code%22%7D'
echo "config      $(mk oidcc-config-certification-test-plan '' config.json)"
echo "basic       $(mk oidcc-basic-certification-test-plan "$BASIC" basic.json)"
echo "rp-logout   $(mk oidcc-rp-initiated-logout-certification-test-plan "$LOGOUT" basic.json)"
echo "backchannel $(mk oidcc-backchannel-rp-initiated-logout-certification-test-plan "$LOGOUT" backchannel.json)"
