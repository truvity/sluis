{{/*
The chart's name, as the `app.kubernetes.io/name` label and the prefix of the
controllers' selector labels. `nameOverride` exists for one reason: a
Deployment's selector is immutable, so an installation moving from the
access-issuer chart sets it to `access-issuer` and keeps its selectors.
*/}}
{{- define "sluis.name" -}}
{{- .Values.nameOverride | default .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
The prefix of every object the chart renders, and the value `config.release`
must carry. `fullnameOverride` keeps the names of an installation moving from
the access-issuer chart: set it to the old full name and nothing is renamed.
*/}}
{{- define "sluis.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else if eq .Release.Name .Chart.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "sluis.labels" -}}
app.kubernetes.io/name: {{ include "sluis.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "sluis.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sluis.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "sluis.serviceAccountName" -}}
{{- .Values.serviceAccount.name | default (include "sluis.fullname" .) }}
{{- end }}

{{/*
Non-empty when the config signs with AWS KMS (`signingKey.kmsWrapped` or
`signingKey.kms`, or `adapters.signing` naming one of them): the keys are made
and held by KMS, so there is no key file to mount and nothing for cert-manager
to issue. Then the chart renders no signing Certificate and mounts no signing
Secret, and `config.signingKey.file` must be unset.
*/}}
{{- define "sluis.remoteSigning" -}}
{{- $s := dig "signingKey" dict .Values.config -}}
{{- if or $s.kmsWrapped $s.kms (has (dig "adapters" "signing" "adapter" "" .Values.config) (list "kms" "kms-wrapped")) }}yes{{ end -}}
{{- end }}

{{/*
The state adapter the config chooses: `adapters.state`, else `ports.adapter`,
else the one a preset names (the AWS ones keep state in DynamoDB), else the
legacy default. Only what the chart needs to know: whether the leases are shared.
*/}}
{{- define "sluis.stateAdapter" -}}
{{- $c := .Values.config -}}
{{- $preset := dig "preset" "" $c -}}
{{- $fromPreset := ternary "dynamodb" "" (has $preset (list "k8s-aws" "aws-eks" "aws-serverless" "aws-hybrid")) -}}
{{- dig "adapters" "state" "adapter" "" $c | default (dig "ports" "adapter" "" $c) | default $fromPreset | default "legacy" -}}
{{- end }}

{{/*
How often the kms-wrapped adapter generates a key pair, in seconds: the
config's `signingKey.kmsWrapped.rotateEvery`, or `adapters.signing.settings`'s,
or the adapter's default (24h). Zero when signing is not kms-wrapped.
*/}}
{{- define "sluis.wrappedRotateSeconds" -}}
{{- $c := .Values.config -}}
{{- $wrapped := dig "signingKey" "kmsWrapped" dict $c -}}
{{- $viaAdapter := eq (dig "adapters" "signing" "adapter" "" $c) "kms-wrapped" -}}
{{- if or $wrapped $viaAdapter -}}
{{- $every := $wrapped.rotateEvery | default (dig "adapters" "signing" "settings" "rotateEvery" "" $c) | default "24h" -}}
{{- if not (regexMatch "^[0-9]+(s|m|h)$" $every) -}}
{{- fail (printf "the kms-wrapped rotateEvery %q is not one number and one unit (s, m or h), which the chart cannot read: write it so (24h), or set alerts.rules.signingKeyRotationStalled.maxAgeSeconds yourself" $every) -}}
{{- end -}}
{{- div (include "sluis.simpleDurationNanos" $every | int64) 1000000000 -}}
{{- else -}}0{{- end -}}
{{- end }}

{{/*
The Secret the signing key is read from: one external-secrets delivered,
or the one cert-manager issues for this release.
*/}}
{{- define "sluis.signingKeySecret" -}}
{{- .Values.signingKey.existingSecret | default (printf "%s-signing-key" (include "sluis.fullname" .)) }}
{{- end }}

{{/*
The name this installation's objects carry: the config's `release`, which the
binary defaults to sluis. The chart requires it to be the release's
full name (see sluis.checks): the Role names, the ConfigMaps and the
Secrets the service writes and the controllers read are `<full name>-...`.
*/}}
{{- define "sluis.release" -}}
{{- dig "release" "sluis" .Values.config -}}
{{- end }}

{{/*
The audience a workload token must be minted for: the config's
`exchange.audience`, which the binary defaults to the release name so that
two issuers in one cluster cannot accept each other's exchange proofs. The
controllers' projected tokens are minted for it.
*/}}
{{- define "sluis.exchangeAudience" -}}
{{- (dig "exchange" "audience" "" .Values.config) | default (include "sluis.release" .) }}
{{- end }}

{{/*
The listeners' ports, from the addresses the config gives. The config says
`host:port`; what the chart needs is the port, for the container, the Service,
the NetworkPolicy and the routes. A port outside 1-65535 is refused here, at
render: the binary would start on a port nothing can reach.
*/}}
{{- define "sluis.portOf" -}}
{{- $port := regexFind "[0-9]+$" .address | int -}}
{{- if or (lt $port 1) (gt $port 65535) -}}
{{- fail (printf "%s: the port in %q must be between 1 and 65535" .path .address) -}}
{{- end -}}
{{- $port -}}
{{- end -}}

{{- define "sluis.port" -}}
{{- include "sluis.portOf" (dict "path" "config.listen.address" "address" (dig "listen" "address" ":8080" .Values.config)) -}}
{{- end -}}

{{- define "sluis.healthPort" -}}
{{- include "sluis.portOf" (dict "path" "config.probes.address" "address" (dig "probes" "address" ":7070" .Values.config)) -}}
{{- end -}}

{{/*
Every declared client by id: the policy's own `clients` and the access
document's, one table, because a client declared either way is projected its
secret the same way. Returned as YAML, since a template returns a string.
*/}}
{{- define "sluis.declaredClients" -}}
{{- toYaml (.Values.policy.clients | default dict) }}
{{- end }}

{{/*
sluis.document: a document as the chart renders it, its apiVersion first:
sluis.truvity.github.io/<kind>/v<N> (the service document, `sluis`, is v3;
the policy is v2). Takes (dict "kind" "sluis" "doc" <its values>).
*/}}
{{- define "sluis.document" -}}
{{- $d := omit (.doc | default dict) "apiVersion" -}}
apiVersion: sluis.truvity.github.io/{{ .kind }}/{{ if eq .kind "sluis" }}v3{{ else }}v2{{ end }}
{{- if $d }}
{{ toYaml $d }}
{{- end }}
{{- end }}

{{/*
sluis.policyDocument: the policy document. `policy` as the values give it,
with the sections the chart's own values fill: `exchange.clusters` and
`exchange.aws` from `exchange`, the catalogues from `githubApps.catalogue`
and `slackApps` (each entry without its `push`, which is a chart-side
instruction to External Secrets and not part of an App). A section written
both ways is refused: one place says it.
*/}}
{{- define "sluis.policyDocument" -}}
{{- $doc := deepCopy (.Values.policy | default dict) -}}
{{- if $doc.clients -}}
{{- $clients := dict -}}
{{- range $id, $c := $doc.clients -}}
{{- $_ := set $clients $id (omit $c "secretKey") -}}
{{- end -}}
{{- $_ := set $doc "clients" $clients -}}
{{- end -}}
{{- $exchange := deepCopy (dig "exchange" dict $doc) -}}
{{- if .Values.exchange.clusters -}}
{{- if hasKey $exchange "clusters" -}}
{{- fail "exchange.clusters and policy.exchange.clusters are both set: say the clusters once, in one of them" -}}
{{- end -}}
{{- $rows := list -}}
{{- range .Values.exchange.clusters -}}
{{- $rows = append $rows (pick . "name" "issuer" "jwksUri") -}}
{{- end -}}
{{- $_ := set $exchange "clusters" $rows -}}
{{- end -}}
{{- if .Values.exchange.aws.accounts -}}
{{- if hasKey $exchange "aws" -}}
{{- fail "exchange.aws and policy.exchange.aws are both set: say the AWS accounts once, in one of them" -}}
{{- end -}}
{{- $aws := dict "audience" (.Values.exchange.aws.audience | default (required "config.issuerURL is required" .Values.config.issuerURL | trimSuffix "/")) "maxAge" (.Values.exchange.aws.maxAge | default "5m") "accounts" .Values.exchange.aws.accounts -}}
{{- $_ := set $exchange "aws" $aws -}}
{{- end -}}
{{- if $exchange -}}
{{- $_ := set $doc "exchange" $exchange -}}
{{- end -}}
{{- $apps := deepCopy (dig "apps" dict $doc) -}}
{{- if .Values.githubApps.catalogue -}}
{{- $github := deepCopy (dig "github" dict $apps) -}}
{{- if hasKey $github "catalogue" -}}
{{- fail "githubApps.catalogue and policy.apps.github.catalogue are both set: declare the Apps once, in one of them" -}}
{{- end -}}
{{- $list := list -}}
{{- range .Values.githubApps.catalogue -}}
{{- $list = append $list (omit . "push") -}}
{{- end -}}
{{- $_ := set $github "catalogue" $list -}}
{{- $_ := set $apps "github" $github -}}
{{- end -}}
{{- if .Values.slackApps -}}
{{- $slack := deepCopy (dig "slack" dict $apps) -}}
{{- if hasKey $slack "catalogue" -}}
{{- fail "slackApps and policy.apps.slack.catalogue are both set: declare the Apps once, in one of them" -}}
{{- end -}}
{{- $list := list -}}
{{- range .Values.slackApps -}}
{{- $list = append $list (omit . "push") -}}
{{- end -}}
{{- $_ := set $slack "catalogue" $list -}}
{{- $_ := set $apps "slack" $slack -}}
{{- end -}}
{{- if $apps -}}
{{- $_ := set $doc "apps" $apps -}}
{{- end -}}
{{- include "sluis.document" (dict "kind" "policy" "doc" $doc) -}}
{{- end }}

{{/*
Non-empty when any declared client carries a secret, which is what decides
whether the client-secrets volume is rendered at all. A deployment whose
clients are all public or exchange-only mounts nothing.
*/}}
{{- define "sluis.secretFiles" -}}
{{- if include "sluis.documentsMode" . -}}
{{- if and .Values.secrets (eq (dig "secrets" "source" "env" .Values.config) "file") }}yes{{ end -}}
{{- else if or .Values.secrets (include "sluis.confidentialClients" .) }}yes{{ end }}
{{- end }}

{{- define "sluis.confidentialClients" -}}
{{- /*
  Values mode only: a client's `secret` is the name of the Kubernetes Secret that
  holds it. With documents (`documents.service`) it is the secret's NAME
  (clients/<id>/secret), the same string the secrets source resolves, so there
  is nothing to read a Secret name from; the Secret behind each name is declared
  in `secrets` instead.
*/ -}}
{{- if not (include "sluis.documentsMode" .) -}}
{{- range $id, $client := (include "sluis.declaredClients" . | fromYaml) }}
{{- if $client.secret }}yes{{ end }}
{{- end }}
{{- end }}
{{- end }}

{{/*
The console's mount, with any trailing slash removed.

`/console` and `/console/` are the same place to a person and the chart
took them as different: the route matched "/console/" exactly and then
redirected to "/console//", which is a path the console does not serve.
Nothing rejected it, because both are legal strings.

The value is written both ways across our own configuration -- a declared
client carries `prefix: /console` and `mount: /console/` -- so normalising
here is what keeps either spelling working. The Go side already trims it.
*/}}
{{- define "sluis.consoleMount" -}}
{{- $mount := .Values.console.mount | default "" | trimSuffix "/" -}}
{{- $mount -}}
{{- end -}}

{{/*
Whether a controller runs in this process: its section is in `config.controllers`
(an empty section is a controller on, with the defaults). Takes (dict "root" $
"kind" "github"); returns "true" or nothing.
*/}}
{{- define "sluis.controllerOn" -}}
{{- $all := dig "controllers" dict .root.Values.config -}}
{{- if and (hasKey $all .kind) (kindIs "map" (get $all .kind)) }}true{{ end -}}
{{- end -}}

{{/*
Where the issuer's routes attach. route.parentRefs, when given, is used as
written -- the platform's ListenerSet carrying route.host -- and the chart
then renders no Gateway or Certificate of its own: the listener and its
certificate belong to whatever that parent is. Otherwise the chart's own
Gateway listener, as before.
*/}}
{{- define "sluis.parentRefs" -}}
{{- if .Values.route.parentRefs -}}
{{- range .Values.route.parentRefs }}
{{- if not .name }}{{ fail "route.parentRefs: every entry needs a name" }}{{ end }}
{{- end -}}
{{ toYaml .Values.route.parentRefs }}
{{- else -}}
- group: gateway.networking.k8s.io
  kind: Gateway
  name: {{ include "sluis.fullname" . }}
  sectionName: issuer
{{- end -}}
{{- end -}}

{{/*
sluis.privateKey refuses a key the issuer could not use.

A size that does not belong to its algorithm renders a Certificate
cert-manager declines, and an ECDSA key in PKCS1 cannot be encoded at all --
both of which fail after the render, on a CertificateRequest nobody is
watching, with the listener simply dark. The schema states the shape; this
states the combination, because a JSON Schema `allOf` reports only that
`allOf` failed and three different mistakes would read the same.

Takes a dict: `spec` (the privateKey map) and `path` (where to say it is).
*/}}
{{- define "sluis.privateKey" -}}
{{- $spec := .spec | default dict -}}
{{- $alg := $spec.algorithm | default "" -}}
{{- $size := $spec.size | default 0 -}}
{{- $encoding := $spec.encoding | default "" -}}
{{- if and (eq $alg "ECDSA") $size (not (has (int $size) (list 256 384 521))) -}}
{{- fail (printf "%s: an ECDSA key takes size 256, 384 or 521, not %v -- the curve decides the algorithm, so P-256 signs ES256, P-384 ES384 and P-521 ES512" .path $size) -}}
{{- end -}}
{{- if and (eq $alg "RSA") $size (not (has (int $size) (list 2048 3072 4096))) -}}
{{- fail (printf "%s: an RSA key takes size 2048, 3072 or 4096, not %v" .path $size) -}}
{{- end -}}
{{- if and (eq $alg "ECDSA") (eq $encoding "PKCS1") -}}
{{- fail (printf "%s: PKCS1 encodes only RSA keys; an ECDSA key is PKCS8" .path) -}}
{{- end -}}
{{- end -}}

{{/*
sluis.signingAlgorithmOf is the JOSE algorithm one private key
spec signs with: RSA is always RS256, and ECDSA's SIZE decides ES256,
ES384 or ES512 -- the pairing RFC 7518 fixes, not a preference, and the
same rule internal/issuer.signatureAlgorithm applies in Go. It is what
tells two `signingKey.additional` entries apart for the "one key per
algorithm" refusal below: `algorithm: ECDSA` alone is not unique enough,
since a 256 and a 384 size both say ECDSA but sign two different
algorithms, and two 384s say it twice.

Takes the privateKey spec directly (the certificate or one
`signingKey.additional` entry), not the {spec,path} wrapper
sluis.privateKey takes. Empty for a spec naming neither, which
sluis.privateKey has already refused by the time this is asked to
name one.
*/}}
{{- define "sluis.signingAlgorithmOf" -}}
{{- $spec := . | default dict -}}
{{- $alg := $spec.algorithm | default "" -}}
{{- $size := int ($spec.size | default 0) -}}
{{- if eq $alg "RSA" -}}
RS256
{{- else if eq $alg "ECDSA" -}}
{{- if eq $size 256 -}}ES256
{{- else if eq $size 384 -}}ES384
{{- else if eq $size 521 -}}ES512
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
sluis.additionalSigningKeyName is the Secret, volume and mount
name for one `signingKey.additional` entry: the algorithm it signs with,
lowercased -- `signing-key-rs256`, `signing-key-es256` -- which is what
[sluis.signingAlgorithmOf] already guarantees is unique across
every entry (see the refusal in templates/signing-key.yaml). Named by
what it SIGNS rather than by position, so adding or reordering entries in
`values.yaml` renames nothing already running.

Takes one `signingKey.additional` entry.
*/}}
{{- define "sluis.additionalSigningKeyName" -}}
signing-key-{{ include "sluis.signingAlgorithmOf" . | lower }}
{{- end -}}

{{/*
sluis.additionalSigningKeyFiles is `config.signingKey.additionalFiles`: every
`signingKey.additional` entry's mounted key file, comma-joined, in the
order they are declared. Empty when there are none, which is every
deployment before per-audience signing existed.
*/}}
{{- define "sluis.additionalSigningKeyFiles" -}}
{{- $paths := list -}}
{{- range .Values.signingKey.additional -}}
{{- $name := include "sluis.additionalSigningKeyName" . -}}
{{- $paths = append $paths (printf "/var/run/access-issuer/%s/%s" $name (.key | default "tls.key")) -}}
{{- end -}}
{{- join "," $paths -}}
{{- end -}}

{{/*
sluis.simpleDurationNanos converts a SINGLE-UNIT duration string
("24h", "90m", "500ms") to nanoseconds, as an integer, for comparing two
durations written in different units. It is not a general parser: a
compound duration ("1h30m") is not this shape, and a caller must check
that with the pattern below before calling it.
*/}}
{{- define "sluis.simpleDurationNanos" -}}
{{- $num := regexFind "^[0-9]+" . | int64 -}}
{{- $unit := regexFind "[a-zµ]+$" . -}}
{{- if eq $unit "h" -}}{{ mul $num 3600000000000 }}
{{- else if eq $unit "m" -}}{{ mul $num 60000000000 }}
{{- else if eq $unit "s" -}}{{ mul $num 1000000000 }}
{{- else if eq $unit "ms" -}}{{ mul $num 1000000 }}
{{- else if or (eq $unit "us") (eq $unit "µs") -}}{{ mul $num 1000 }}
{{- else -}}{{ $num }}
{{- end -}}
{{- end -}}

{{/*
sluis.validateLifetimes refuses config.lifetimes.absolute: zero (a
session that ends before or the instant it begins is not a limit, it is a
login that can never complete) or, for the common case of a single-unit
duration, shorter than config.lifetimes.token (an access token cannot outlive
the session that grants it). Unset is the binary's default: 1h and 24h.

A compound duration ("1h30m") is not compared against the token lifetime:
parsing one fully needs a real duration parser, which Helm's template
language has none of, and a comparison that WRONGLY refuses a valid value
is worse than one silently skipped. The running service checks this
exactly, with Go's time.ParseDuration, and refuses to start if it is
wrong -- see issuerapp.FromConfig. The zero check does not have this problem:
"contains no digit but 0" is true or false regardless of how many units
a duration mixes.
*/}}
{{- define "sluis.validateLifetimes" -}}
{{- $l := dig "lifetimes" dict .Values.config -}}
{{- $absolute := $l.absolute | default "24h" -}}
{{- $token := $l.token | default "1h" -}}
{{- $simple := "^[0-9]+(ns|us|µs|ms|s|m|h)$" -}}
{{- if not (regexMatch "[1-9]" $absolute) -}}
{{- fail (printf "config.lifetimes.absolute: %q is zero -- a session has to end SOMETIME after sign-in, not before it" $absolute) -}}
{{- end -}}
{{- if and (regexMatch $simple $absolute) (regexMatch $simple $token) -}}
{{- $absoluteNanos := include "sluis.simpleDurationNanos" $absolute | int64 -}}
{{- $tokenNanos := include "sluis.simpleDurationNanos" $token | int64 -}}
{{- if lt $absoluteNanos $tokenNanos -}}
{{- fail (printf "config.lifetimes.absolute (%s) must be at least config.lifetimes.token (%s): an access token cannot outlive the session that grants it" $absolute $token) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Whether a component's config connects it to an audit installation: the
address its receiver serves. One address is the whole connection. The receiver
takes the records and answers RegisterCatalogue on the same port, because an
installation belongs to one application and a registry of its own would be a
Deployment for a single call.

Takes the component's config.
*/}}
{{- define "sluis.auditConnected" -}}
{{- if (dig "audit" "writer" "" .) }}true{{ end -}}
{{- end -}}

{{/*
The mount and the projected token the installation knows a workload by, with
its audience. There is nothing else to mount: a record that cannot be
delivered waits in the emitter's own queue, in memory, and the pod's disk
holds none of the trail. Both take (dict "root" $ "cfg" <the component's config>).
*/}}
{{- define "sluis.auditMounts" -}}
{{- if include "sluis.auditConnected" .cfg }}
- name: audit-token
  mountPath: /var/run/audit
  readOnly: true
{{- end }}
{{- end -}}

{{- define "sluis.auditVolumes" -}}
{{- if include "sluis.auditConnected" .cfg }}
- name: audit-token
  projected:
    sources:
      - serviceAccountToken:
          audience: {{ .root.Values.audit.token.audience | quote }}
          expirationSeconds: {{ .root.Values.audit.token.expirationSeconds }}
          path: token
{{- end }}
{{- end -}}

{{/*
What the exports need on the pod (docs/decisions/0034): the CA that signs
OpenBao's certificate, and the projected token the `jwt` login presents. Both
are optional and independent. Takes the root.
*/}}
{{- define "sluis.exportsMounts" -}}
{{- if .Values.exports.openbao.caBundle }}
- name: openbao-ca
  mountPath: /var/run/access-issuer/openbao-ca
  readOnly: true
{{- end }}
{{- if .Values.exports.openbao.token.audience }}
- name: openbao-token
  mountPath: /var/run/openbao
  readOnly: true
{{- end }}
{{- end -}}

{{- define "sluis.exportsVolumes" -}}
{{- if .Values.exports.openbao.caBundle }}
- name: openbao-ca
  configMap:
    name: {{ include "sluis.fullname" . }}-openbao-ca
{{- end }}
{{- if .Values.exports.openbao.token.audience }}
- name: openbao-token
  projected:
    sources:
      - serviceAccountToken:
          audience: {{ .Values.exports.openbao.token.audience | quote }}
          expirationSeconds: {{ .Values.exports.openbao.token.expirationSeconds }}
          path: token
{{- end }}
{{- end -}}

{{/*
The console's secret-store view was removed in v1.30.0. Refuse render if an
old configuration tries to activate it, with a message pointing to the
migration.
*/}}
{{- define "sluis.validateSecretManagers" -}}
{{- if .Values.secretManagers }}{{ fail "secretManagers was removed in v1.30.0: delete this key from your values. To sign in to OpenBAO, use its own OIDC login (see docs/how-to/connect/openbao.md)." }}{{ end -}}
{{- end -}}

{{- /* Alert mode. A rule's labels: the routing labels the caller sets for every
rule, then the rule's own severity, then anything the rule's `labels` adds. */ -}}
{{- define "sluis.ruleLabels" -}}
{{- $l := mergeOverwrite (deepCopy (.root.Values.alerts.ruleLabels | default dict)) (dict "severity" .cfg.severity) (deepCopy (.cfg.labels | default dict)) -}}
{{- toYaml $l -}}
{{- end -}}
{{- define "sluis.runbook" -}}
{{- with .root.Values.alerts.runbookBaseUrl -}}
runbook_url: {{ printf "%s#%s" . (lower $.alert) | quote }}
{{- end -}}
{{- end -}}

{{/*
sluis.otelEnv: the OpenTelemetry SDK environment of one pod, as list
items, or nothing when no endpoint is set (ADR 0006: a service exports only
when an endpoint is named). Takes (dict "root" $ "service" "<service.name>").
extraEnv comes last, sorted, so the file is stable; it cannot carry the
endpoint (sluis.validateTelemetry).
*/}}
{{- define "sluis.otelEnv" -}}
{{- $t := .root.Values.telemetry | default dict -}}
{{- $o := $t.otlp | default dict -}}
{{- if $o.endpoint }}
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: {{ $o.endpoint | quote }}
- name: OTEL_EXPORTER_OTLP_PROTOCOL
  value: {{ $o.protocol | default "http/protobuf" | quote }}
- name: OTEL_SERVICE_NAME
  value: {{ .service | quote }}
{{- range $name := keys ($o.extraEnv | default dict) | sortAlpha }}
- name: {{ $name }}
  value: {{ get $o.extraEnv $name | toString | quote }}
{{- end }}
{{- end }}
{{- end -}}

{{/*
sluis.documentsMode: non-empty when the release is given the rendered documents
(`documents.service` and `documents.policy`, from `sluisctl render`) instead of
`config` and `policy`. Then the two documents are the ConfigMaps' content, byte
for byte, and the chart only holds them to what it mounts.
*/}}
{{- define "sluis.documentsMode" -}}
{{- if or .Values.documents.service .Values.documents.policy }}yes{{ end -}}
{{- end }}

{{/*
sluis.prepare: every template file starts with it. In documents mode it makes
`.Values.config` and `.Values.policy` BE the two documents (parsed, and never
written back: the ConfigMaps carry the strings as given), so that every check
and every mount decision below reads the document the service will read, and
not a default of the values around it. The exchange's clusters are read from the
policy document for the same reason. Idempotent: the first call does it, and a
marker says so. Refuses what would be said twice: `policy`, `exchange.clusters`
and `exchange.aws.accounts` beside the documents. `config` is never read.
*/}}
{{- define "sluis.prepare" -}}
{{- if and (include "sluis.documentsMode" .) (not (hasKey .Values "documentsPrepared")) -}}
{{- if not (and .Values.documents.service .Values.documents.policy) -}}
{{- fail "documents.service and documents.policy go together: the rendered service document and the rendered policy document of one installation (sluisctl render)" -}}
{{- end -}}
{{- if or .Values.policy .Values.exchange.clusters .Values.exchange.aws.accounts -}}
{{- fail "documents.service and documents.policy are set, and so are policy, exchange.clusters or exchange.aws.accounts: the documents hold them, say each once (the chart's `config` is not read in this mode)" -}}
{{- end -}}
{{- $service := fromYaml .Values.documents.service -}}
{{- $policy := fromYaml .Values.documents.policy -}}
{{- if hasKey $service "Error" -}}
{{- fail (printf "documents.service is not YAML: %s" $service.Error) -}}
{{- end -}}
{{- if hasKey $policy "Error" -}}
{{- fail (printf "documents.policy is not YAML: %s" $policy.Error) -}}
{{- end -}}
{{- if ne ($service.apiVersion | default "") "sluis.truvity.github.io/sluis/v3" -}}
{{- fail (printf "documents.service must be the service document, apiVersion sluis.truvity.github.io/sluis/v3 (got %q): render it with sluisctl render" ($service.apiVersion | default "")) -}}
{{- end -}}
{{- if ne ($policy.apiVersion | default "") "sluis.truvity.github.io/policy/v2" -}}
{{- fail (printf "documents.policy must be the policy document, apiVersion sluis.truvity.github.io/policy/v2 (got %q): render it with sluisctl render" ($policy.apiVersion | default "")) -}}
{{- end -}}
{{- include "sluis.documentGuards" $service -}}
{{- $_ := set .Values "config" $service -}}
{{- $_ := set .Values "policy" $policy -}}
{{- $_ := set .Values.exchange "clusters" (dig "exchange" "clusters" list $policy) -}}
{{- $_ := set .Values "documentsPrepared" true -}}
{{- end -}}
{{- end }}

{{/*
sluis.credentialKeys: the paths of the keys under a value that would hold a
credential's value: a key naming a token, password, secret or key material that
is not a NAME (`...Secret`) or a path (`...File`). Takes (dict "v" <value> "at"
<path>); returns one path per line.
*/}}
{{- define "sluis.credentialKeys" -}}
{{- $at := .at -}}
{{- if kindIs "map" .v -}}
{{- range $k, $x := .v -}}
{{- $lower := lower $k -}}
{{- if not (or (hasSuffix "file" $lower) (hasSuffix "secret" $lower)) -}}
{{- range $w := list "token" "password" "passwd" "secret" "credential" "privatekey" "apikey" "accesskey" -}}
{{- if contains $w $lower }}{{ printf "%s.%s" $at $k }}
{{ end -}}
{{- end -}}
{{- end -}}
{{ include "sluis.credentialKeys" (dict "v" $x "at" (printf "%s.%s" $at $k)) -}}
{{- end -}}
{{- else if kindIs "slice" .v -}}
{{- range $i, $x := .v -}}
{{ include "sluis.credentialKeys" (dict "v" $x "at" (printf "%s[%d]" $at $i)) -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
sluis.documentGuards: what the chart can still refuse in a rendered document
Helm cannot run the loader, so a hand-edited document is held to the two rules a
reviewer would not see: an OpenBao is reached over https only (a login token
crosses the connection), and no adapter setting carries a credential's value.
Takes the parsed service document.
*/}}
{{- define "sluis.documentGuards" -}}
{{- range $concern, $choice := (dig "adapters" dict .) -}}
{{- $settings := $choice.settings | default dict -}}
{{- if and (eq ($choice.adapter | default "") "openbao") (not (hasPrefix "https://" ($settings.address | default ""))) -}}
{{- fail (printf "documents.service: adapters.%s.settings.address must be an https URL: a login token crosses this connection (documents come from `sluisctl render`, which refuses it)" $concern) -}}
{{- end -}}
{{- $bad := trim (include "sluis.credentialKeys" (dict "v" $settings "at" (printf "adapters.%s.settings" $concern))) -}}
{{- if $bad -}}
{{- fail (printf "documents.service: %s names a credential: a document carries the NAME of a secret (...Secret) or a file (...File), never a value (documents come from `sluisctl render`, which refuses it)" ($bad | replace "\n" ", ")) -}}
{{- end -}}
{{- end -}}
{{- $ports := dig "ports" "export" "openbao" dict . -}}
{{- if and $ports.address (not (hasPrefix "https://" $ports.address)) -}}
{{- fail "documents.service: ports.export.openbao.address must be an https URL: a login token crosses this connection" -}}
{{- end -}}
{{- end }}
