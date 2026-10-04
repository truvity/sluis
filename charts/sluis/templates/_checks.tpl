{{/*
What the chart holds a config to.

The config is rendered as it stands, so the chart cannot compute a value into
it. What it can do is refuse a config that disagrees with what the chart itself
renders and mounts: a path where the chart mounts nothing, a release name that
is not the one the objects carry, a console address that is not this release's
Service. Each of these used to be set by the chart and could not be wrong; each
is now said in the config and checked here, and each refusal names the value to
write.

Two kinds of check live here and nowhere else:
  - what spans a component's config and the values around it (this file);
  - what is local to one config is the schema's, or the binary's own start-up
    (schemas/config/*.schema.json, internal/config, issuerapp.FromConfig).
*/}}

{{/*
sluis.expectPath: a config key names a file or directory the chart
mounts. Takes (dict "key" "config.exchange.clustersFile" "got" <the config's
value> "want" <where the chart mounts it> "source" "exchange.clusters"
"present" <whether the chart renders and mounts it>).

Present: the key must be exactly where the chart mounts it. Absent: the key
must be unset, because it names a file nothing provides.
*/}}
{{- define "sluis.expectPath" -}}
{{- $got := .got | default "" -}}
{{- if and .present (ne $got .want) -}}
{{- fail (printf "%s must be %s, where the chart mounts what %s renders (got %q)" .key .want .source $got) -}}
{{- end -}}
{{- if and (not .present) $got -}}
{{- fail (printf "%s is %q and the chart renders nothing there: set %s, which the chart mounts at %s, or remove the key" .key $got .source .want) -}}
{{- end -}}
{{- end -}}

{{/*
sluis.expectRelease: a config's `release` is the name its objects
carry. Takes (dict "key" "config.release" "root" $ "got" <the config's release>).
*/}}
{{- define "sluis.expectRelease" -}}
{{- $want := include "sluis.fullname" .root -}}
{{- $got := .got | default "sluis" -}}
{{- if ne $got $want -}}
{{- fail (printf "%s must be %q, this release's full name: the Roles, ConfigMaps and Secrets the service and the controllers share are named from it (got %q; unset is sluis; an installation moving from the access-issuer chart sets fullnameOverride to its old full name)" .key $want $got) -}}
{{- end -}}
{{- end -}}

{{/*
sluis.expectAudit: a config that names an audit receiver must say where
the chart mounts the projected token it presents. Without a receiver there is
nothing to present it to, and the key may be unset or left at the default.
Takes (dict "key" "config.audit.tokenFile" "cfg" <the component's config>).
*/}}
{{- define "sluis.expectAudit" -}}
{{- $got := dig "audit" "tokenFile" "" .cfg -}}
{{- if dig "audit" "writer" "" .cfg -}}
{{- include "sluis.expectPath" (dict "key" .key "got" $got "want" "/var/run/audit/token" "source" "audit.token" "present" true) -}}
{{- else if and $got (ne $got "/var/run/audit/token") -}}
{{- fail (printf "%s is %q and no audit receiver is named: set audit.writer, whose token the chart mounts at /var/run/audit/token, or remove the key" .key $got) -}}
{{- end -}}
{{- end -}}

{{/*
sluis.checks: everything the service's config must agree with.
*/}}
{{- define "sluis.checks" -}}
{{- $c := .Values.config -}}
{{- $_ := required "config.issuerURL is required: it is baked into every token and every relying party's trust" $c.issuerURL -}}
{{- include "sluis.validateLifetimes" . -}}
{{- include "sluis.validateSecretManagers" . -}}
{{- include "sluis.validateTelemetry" . -}}
{{- $_ := include "sluis.port" . -}}
{{- $_ := include "sluis.healthPort" . -}}
{{- include "sluis.expectRelease" (dict "key" "config.release" "root" . "got" $c.release) -}}
{{- include "sluis.expectPath" (dict "key" "config.policyDir" "got" $c.policyDir "want" "/var/run/access-issuer/policy" "source" "policy" "present" true) -}}
{{- $signing := dig "signingKey" dict $c -}}
{{- include "sluis.expectPath" (dict "key" "config.signingKey.file" "got" $signing.file "want" (printf "/var/run/access-issuer/signing-key/%s" .Values.signingKey.key) "source" "signingKey.key" "present" true) -}}
{{- $additional := include "sluis.additionalSigningKeyFiles" . -}}
{{- if ne (join "," ($signing.additionalFiles | default list)) $additional -}}
{{- fail (printf "config.signingKey.additionalFiles must be [%s], one file per signingKey.additional entry in the order they are declared (got [%s])" $additional (join ", " ($signing.additionalFiles | default list))) -}}
{{- end -}}
{{- $exchange := dig "exchange" dict $c -}}
{{- include "sluis.expectPath" (dict "key" "config.exchange.clustersFile" "got" $exchange.clustersFile "want" "/var/run/access-issuer/clusters.yaml" "source" "exchange.clusters" "present" (not (empty .Values.exchange.clusters))) -}}
{{- include "sluis.expectPath" (dict "key" "config.exchange.awsFile" "got" $exchange.awsFile "want" "/var/run/access-issuer/aws.yaml" "source" "exchange.aws.accounts" "present" (not (empty .Values.exchange.aws.accounts))) -}}
{{- include "sluis.expectPath" (dict "key" "config.overlayFile" "got" $c.overlayFile "want" "/var/run/access-issuer/directory/overlay.yaml" "source" "directory.workspaces" "present" (not (empty .Values.directory.workspaces))) -}}
{{- include "sluis.expectPath" (dict "key" "config.github.catalogueFile" "got" (dig "github" "catalogueFile" "" $c) "want" "/var/run/access-issuer/github-apps-catalogue.yaml" "source" "githubApps.catalogue" "present" (not (empty .Values.githubApps.catalogue))) -}}
{{- include "sluis.expectPath" (dict "key" "config.slack.catalogueFile" "got" (dig "slack" "catalogueFile" "" $c) "want" "/var/run/access-issuer/slack-apps-catalogue.yaml" "source" "slackApps" "present" (not (empty .Values.slackApps))) -}}
{{- include "sluis.expectPath" (dict "key" "config.clientSecretsDir" "got" $c.clientSecretsDir "want" "/var/run/access-issuer/clients" "source" "policy.clients[].secret" "present" (not (empty (include "sluis.confidentialClients" .)))) -}}
{{- include "sluis.expectAudit" (dict "key" "config.audit.tokenFile" "cfg" $c) -}}
{{- $openbao := dig "ports" "export" "openbao" dict $c -}}
{{- include "sluis.expectPath" (dict "key" "config.ports.export.openbao.caFile" "got" $openbao.caFile "want" "/var/run/access-issuer/openbao-ca/ca.pem" "source" "exports.openbao.caBundle" "present" (not (empty .Values.exports.openbao.caBundle))) -}}
{{- include "sluis.expectPath" (dict "key" "config.ports.export.openbao.auth.tokenFile" "got" (dig "auth" "tokenFile" "" $openbao) "want" "/var/run/openbao/token" "source" "exports.openbao.token.audience" "present" (not (empty .Values.exports.openbao.token.audience))) -}}
{{- if and $c.exports (not (dig "ports" "export" "adapter" "" $c)) -}}
{{- fail "config.exports names secrets to copy and config.ports.export names nowhere to copy them to: set config.ports.export (adapter: openbao, and its address and auth), or remove config.exports" -}}
{{- end -}}
{{- /*
  Recovery is the one thing left that asks the API server, and deliberately
  so: on the day everything else is broken it should depend on nothing but
  the cluster. The chart creates the account, so it has to be named, and the
  process has to know it runs in a cluster.
*/ -}}
{{- $recovery := dig "recovery" dict $c -}}
{{- if $recovery.enabled -}}
{{- if not $c.inCluster -}}
{{- fail "config.recovery.enabled needs config.inCluster: true: recovery proves access to the cluster the pod runs in, and without it the service builds no recovery and says so only in its log" -}}
{{- end -}}
{{- if not $recovery.serviceAccount -}}
{{- fail "config.recovery.enabled needs config.recovery.serviceAccount: the account a recovery token is minted for, which the chart creates and binds to nothing" -}}
{{- end -}}
{{- if not $recovery.audience -}}
{{- fail "config.recovery.enabled needs config.recovery.audience: without one, every mounted ServiceAccount token in the cluster would be a proof" -}}
{{- end -}}
{{- end -}}
{{- /*
  The public URLs were built from the route: the browser reaches the console
  at https://<host><mount>, and the admin-consent callback stays at the
  origin ROOT, registered with every corporate tenant. Served over TLS, the
  cookies say so.
*/ -}}
{{- if .Values.route.host -}}
{{- if and (hasKey $c "secureCookies") (not $c.secureCookies) -}}
{{- fail "config.secureCookies: false, and the service is served over TLS at route.host: its session cookies would be sent without the Secure flag" -}}
{{- end -}}
{{- if .Values.console.mount -}}
{{- $root := printf "https://%s" .Values.route.host -}}
{{- if ne ($c.publicRootURL | default "") $root -}}
{{- fail (printf "config.publicRootURL must be %s, the origin route.host serves: the admin-consent callback is registered at the origin root (got %q)" $root ($c.publicRootURL | default "")) -}}
{{- end -}}
{{- $public := printf "%s%s" $root (include "sluis.consoleMount" .) -}}
{{- if ne ($c.publicURL | default "") $public -}}
{{- fail (printf "config.publicURL must be %s, where a browser reaches the console: route.host plus console.mount (got %q)" $public ($c.publicURL | default "")) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
sluis.rosterChecks: what a controller's config must agree with. Takes
(dict "root" $ "name" "controllerGithub" "cfg" <its config> "dir" "github-roster").
*/}}
{{- define "sluis.rosterChecks" -}}
{{- $root := .root -}}
{{- $c := .cfg -}}
{{- $dir := .dir -}}
{{- if not $root.Values.exchange.clusters -}}
{{- fail (printf "%s.enabled needs exchange.clusters to name this cluster: the service verifies the controller's ServiceAccount token against that key set, and with no row it verifies nothing" .name) -}}
{{- end -}}
{{- if not (include "sluis.consoleMount" $root) -}}
{{- fail (printf "%s.enabled needs console.mount: the controller reads the console's API, and with no mount there is none" .name) -}}
{{- end -}}
{{- $want := printf "http://%s.%s.svc:%s%s" (include "sluis.fullname" $root) $root.Release.Namespace (include "sluis.port" $root) (include "sluis.consoleMount" $root) -}}
{{- if not $c.consoleURL -}}
{{- fail (printf "%s.config.consoleURL is required: who holds a group is the console's to answer; write %s, this release's own Service" .name $want) -}}
{{- end -}}
{{- if ne (trimSuffix "/" $c.consoleURL) $want -}}
{{- fail (printf "%s.config.consoleURL must be %s, the console this release serves (got %q)" .name $want $c.consoleURL) -}}
{{- end -}}
{{- include "sluis.expectRelease" (dict "key" (printf "%s.config.release" .name) "root" $root "got" $c.release) -}}
{{- include "sluis.expectPath" (dict "key" (printf "%s.config.policyDir" .name) "got" $c.policyDir "want" (printf "/var/run/%s/policy" $dir) "source" "policy" "present" true) -}}
{{- include "sluis.expectPath" (dict "key" (printf "%s.config.tokenFile" .name) "got" $c.tokenFile "want" (printf "/var/run/secrets/%s/token" $dir) "source" "the projected ServiceAccount token" "present" true) -}}
{{- include "sluis.expectAudit" (dict "key" (printf "%s.config.audit.tokenFile" .name) "cfg" $c) -}}
{{- end -}}

{{/*
sluis.validateTelemetry: what `telemetry.otlp` may say. The endpoint is
an http(s) URL with a host; `extraEnv` holds other OTEL_* variables only, and
never the endpoint, which has a value of its own so that one place sets it.
*/}}
{{- define "sluis.validateTelemetry" -}}
{{- $t := .Values.telemetry | default dict -}}
{{- $o := $t.otlp | default dict -}}
{{- $endpoint := $o.endpoint | default "" -}}
{{- if and $endpoint (not (regexMatch "^https?://[^/?#[:space:]]+" $endpoint)) -}}
{{- fail (printf "telemetry.otlp.endpoint must be an http(s) URL naming the collector or gateway, such as http://gateway.observability.svc:4318 (got %q)" $endpoint) -}}
{{- end -}}
{{- range $name, $_ := ($o.extraEnv | default dict) -}}
{{- if eq $name "OTEL_EXPORTER_OTLP_ENDPOINT" -}}
{{- fail "telemetry.otlp.extraEnv must not carry OTEL_EXPORTER_OTLP_ENDPOINT: set telemetry.otlp.endpoint, which is where the chart takes it from" -}}
{{- end -}}
{{- if not (hasPrefix "OTEL_" $name) -}}
{{- fail (printf "telemetry.otlp.extraEnv holds OpenTelemetry SDK variables only: %q does not start with OTEL_ (a secret reaches a pod through secretEnv, the rest through config)" $name) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
sluis.controllerRollout: what a controller's replicas and strategy must agree
with. Takes (dict "name" "controllerGithub" "v" <its values>).

More than one replica is safe only when the tick leases are shared: a lease is
exclusive across pods only when the State that holds it is. With the `legacy`
adapter a controller keeps its leases in its own memory, so two replicas would
each act on every target and make every change twice (duplicate invitations
and removals, which the platform answers with an error that reads as a
failure). Only `ports.adapter: dynamodb` shares them.
*/}}
{{- define "sluis.controllerRollout" -}}
{{- $adapter := dig "ports" "adapter" "legacy" (.v.config | default dict) -}}
{{- if and (gt (int .v.replicas) 1) (not (has $adapter (list "dynamodb"))) -}}
{{- fail (printf "%s.replicas is %d, which needs the tick leases in a State every replica shares: set %s.config.ports.adapter to dynamodb (it is %q, which keeps the leases in each pod's own memory, so every replica would act on every target and make each change twice). Keep replicas at 1 otherwise: readiness gating and a rolling update already keep the old pod until the new one is Ready" .name (int .v.replicas) .name $adapter) -}}
{{- end -}}
{{- if and (eq .v.strategy.type "Recreate") (gt (int .v.replicas) 1) -}}
{{- fail (printf "%s.strategy.type is Recreate with %d replicas: Recreate stops every replica before the new ones start, which is the outage the replicas exist to prevent. Use RollingUpdate, or set replicas to 1" .name (int .v.replicas)) -}}
{{- end -}}
{{- end -}}
