#!/usr/bin/env bash
#
# The key service for the "services in the cluster" tier: an AWS KMS stand-in
# (LocalStack, the image the box's S3 already pins) with the notary's seal key and the pseudonym key,
# and the Secret that points the pods at it the way a Secret delivered from a
# secret manager would: AWS_ENDPOINT_URL_KMS and AWS_REGION as environment
# variables, read by the SDK's default chain. Nothing reads SSM.
#
# Idempotent. Runs after apply.sh, which makes the namespace and the S3
# credentials Secret.
set -euo pipefail

KCTX=${KCTX:-kind-policy}
NS=${NS:-audit-e2e}
kubectl() { command kubectl --context "$KCTX" "$@"; }

# The same LocalStack digest as the box's S3 (truvity/policy hack/kind/versions.env).
image="localstack/localstack@sha256:3ebc37595918b8accb852f8048fef2aff047d465167edd655528065b07bc364a"

kubectl -n "$NS" apply -f - <<YAML
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kms
spec:
  replicas: 1
  selector:
    matchLabels: { app: kms }
  template:
    metadata:
      labels: { app: kms }
    spec:
      containers:
        - name: kms
          image: $image
          env:
            - { name: SERVICES, value: kms }
          ports:
            - containerPort: 4566
          readinessProbe:
            httpGet: { path: /_localstack/health, port: 4566 }
            periodSeconds: 5
            failureThreshold: 30
---
apiVersion: v1
kind: Service
metadata:
  name: kms
spec:
  selector: { app: kms }
  ports:
    - port: 4566
      targetPort: 4566
YAML
kubectl -n "$NS" rollout status deploy/kms --timeout=5m

# The seal key: ECC_NIST_P384, SIGN_VERIFY, under the alias the chart's values name.
if ! kubectl -n "$NS" exec deploy/kms -- awslocal kms describe-key --key-id alias/audit-seal >/dev/null 2>&1; then
  key_id=$(kubectl -n "$NS" exec deploy/kms -- awslocal kms create-key \
    --key-spec ECC_NIST_P384 --key-usage SIGN_VERIFY --query KeyMetadata.KeyId --output text)
  kubectl -n "$NS" exec deploy/kms -- awslocal kms create-alias --alias-name alias/audit-seal --target-key-id "$key_id"
fi

# The pseudonym key (the per-tenant data keys are wrapped under it) and the
# conceal key (the identity behind a pseudonym is sealed under it): symmetric.
for alias in audit-pseudonym audit-conceal; do
  if ! kubectl -n "$NS" exec deploy/kms -- awslocal kms describe-key --key-id "alias/$alias" >/dev/null 2>&1; then
    key_id=$(kubectl -n "$NS" exec deploy/kms -- awslocal kms create-key --query KeyMetadata.KeyId --output text)
    kubectl -n "$NS" exec deploy/kms -- awslocal kms create-alias --alias-name "alias/$alias" --target-key-id "$key_id"
  fi
done

kubectl -n "$NS" get secret audit-e2e-kms >/dev/null 2>&1 || \
  kubectl -n "$NS" create secret generic audit-e2e-kms \
    --from-literal=AWS_ENDPOINT_URL_KMS="http://kms.$NS.svc:4566" \
    --from-literal=AWS_REGION=us-east-1

echo "the KMS stand-in is in place: alias/audit-seal in $NS"
