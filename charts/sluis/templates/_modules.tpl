{{/*
The per-module workloads (ADR 0071, the v1.76 split, decision D-v176-9): the
chart renders one Deployment, ServiceAccount, ConfigMap, PodDisruptionBudget
and NetworkPolicy per module, and never a single Deployment that runs them all.
The issuer is always there; another module is there when `modules.<m>.enabled`.
*/}}

{{/* "true" when the module is rendered: the issuer always, another when enabled. Takes (dict "root" $ "module" "github"). */}}
{{- define "sluis.moduleOn" -}}
{{- if or (eq .module "issuer") (dig "enabled" false (get .root.Values.modules .module)) }}true{{ end -}}
{{- end }}

{{/*
"true" when the module's pod runs the controller `kind`: the github module the
GitHub controller, the slack module the Slack one. In documents mode the
documents are rendered as they stand, so the issuer's document still names the
controllers it runs and its pod carries their mounts. Takes (dict "root" $
"module" "issuer" "kind" "github").
*/}}
{{- define "sluis.hosts" -}}
{{- if eq .module .kind }}true
{{- else if and (eq .module "issuer") (include "sluis.documentsMode" .root) (include "sluis.controllerOn" (dict "root" .root "kind" .kind)) }}true
{{- end -}}
{{- end }}

{{/* <full name>-<module>: the Deployment, the Service, the document's ConfigMap (with -config) and the default account. */}}
{{- define "sluis.moduleFullname" -}}
{{- printf "%s-%s" (include "sluis.fullname" .root) .module | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/* The module's account: its own `serviceAccount.name`; the issuer's falls back to the release's `serviceAccount.name`. */}}
{{- define "sluis.moduleAccountName" -}}
{{- $own := dig "serviceAccount" "name" "" (get .root.Values.modules .module) -}}
{{- if and (not $own) (eq .module "issuer") -}}
{{- $own = .root.Values.serviceAccount.name -}}
{{- end -}}
{{- $own | default (include "sluis.moduleFullname" .) -}}
{{- end }}

{{/* The modules other modules call, and which therefore answer on /rpc. */}}
{{- define "sluis.callees" -}}
github
slack
google
cloudflare
{{- end }}

{{- define "sluis.isCallee" -}}
{{- if has .module (splitList "\n" (include "sluis.callees" .root)) }}true{{ end -}}
{{- end }}

{{- define "sluis.moduleRPCPort" -}}
{{- dig "rpcPort" 8081 (get .root.Values.modules .module) | int -}}
{{- end }}

{{/* The audience a call to the module carries: its own name unless `audience` says otherwise. */}}
{{- define "sluis.moduleAudience" -}}
{{- dig "audience" "" (get .root.Values.modules .module) | default .module -}}
{{- end }}

{{/* Where the issuer pod mounts the token it presents to a module. */}}
{{- define "sluis.callTokenFile" -}}
{{- printf "/var/run/secrets/sluis/%s/token" .module -}}
{{- end }}

{{/* The health port: the issuer's is config.probes.address; another module's is its document's, else :7070. */}}
{{- define "sluis.moduleHealthPort" -}}
{{- if eq .module "issuer" -}}
{{- include "sluis.healthPort" .root -}}
{{- else -}}
{{- $doc := fromYaml (include "sluis.moduleDocumentBody" .) -}}
{{- include "sluis.portOf" (dict "path" (printf "modules.%s.config.probes.address" .module) "address" (dig "probes" "address" ":7070" $doc)) -}}
{{- end -}}
{{- end }}

{{/* The module's image: its own `image.repository`/`image.tag` over the chart's. */}}
{{- define "sluis.moduleImage" -}}
{{- $own := dig "image" dict (get .root.Values.modules .module) -}}
{{- if $own.repository -}}
{{- printf "%s:%s" $own.repository ($own.tag | default .root.Values.image.tag | default .root.Chart.AppVersion) -}}
{{- else if $own.tag -}}
{{- printf "%s:%s" (regexReplaceAll ":[^:/]*$" (include "sluis.image" (dict "root" .root "module" .module)) "") $own.tag -}}
{{- else -}}
{{- include "sluis.image" (dict "root" .root "module" .module) -}}
{{- end -}}
{{- end }}

{{/*
The module's document as a map, as YAML, before its apiVersion. Takes
(dict "root" $ "module" "github").
  issuer      `config` without `controllers`, and for each enabled module that
              answers calls its own `<module>.remote` ({url, audience,
              tokenFile}), which is how the issuer routes to it. In documents
              mode the document as it stands (see sluis.moduleDocument).
  github      config.controllers.github, with modules.github.config over it, and
              github.serve ({address, audience, issuers}) when unset
  slack       likewise, with slack.serve
  google      modules.google.config
  cloudflare  modules.cloudflare.config, and cloudflare.serve when unset
*/}}
{{- define "sluis.moduleDocumentBody" -}}
{{- $root := .root -}}
{{- $m := get $root.Values.modules .module -}}
{{- $doc := dict -}}
{{- if eq .module "issuer" -}}
{{- $doc = omit (deepCopy $root.Values.config) "controllers" -}}
{{- range $callee := splitList "\n" (include "sluis.callees" $root) -}}
{{- if include "sluis.moduleOn" (dict "root" $root "module" $callee) -}}
{{- $args := dict "root" $root "module" $callee -}}
{{- $remote := dict
      "url" (printf "http://%s.%s.svc:%d" (include "sluis.moduleFullname" $args) $root.Release.Namespace (int (include "sluis.moduleRPCPort" $args)))
      "audience" (include "sluis.moduleAudience" $args)
      "tokenFile" (include "sluis.callTokenFile" $args) -}}
{{- $_ := set $doc $callee (mergeOverwrite (deepCopy (dig $callee dict $doc)) (dict "remote" $remote)) -}}
{{- end -}}
{{- end -}}
{{- else if or (eq .module "github") (eq .module "slack") -}}
{{- $doc = deepCopy (dig "controllers" .module dict $root.Values.config) -}}
{{- $_ := set $doc "consoleURL" (printf "http://%s.%s.svc:%s%s" (include "sluis.fullname" $root) $root.Release.Namespace (include "sluis.port" $root) $root.Values.console.mount) -}}
{{- $doc = mergeOverwrite $doc (deepCopy ($m.config | default dict)) -}}
{{- /* The module answers the issuer on /rpc: the listener, the audience of the token, and the one account let in. */ -}}
{{- $args := dict "root" $root "module" .module -}}
{{- $route := deepCopy (dig .module dict $doc) -}}
{{- $serve := deepCopy (dig "serve" dict $route) -}}
{{- $_ := set $serve "address" ($serve.address | default (printf ":%s" (include "sluis.moduleRPCPort" $args))) -}}
{{- $_ := set $serve "audience" ($serve.audience | default (include "sluis.moduleAudience" $args)) -}}
{{- $_ := set $serve "issuers" ($serve.issuers | default (list (printf "system:serviceaccount:%s:%s" $root.Release.Namespace (include "sluis.moduleAccountName" (dict "root" $root "module" "issuer"))))) -}}
{{- $_ := set $route "serve" $serve -}}
{{- $_ := set $doc .module $route -}}
{{- else -}}
{{- $doc = deepCopy ($m.config | default dict) -}}
{{- if eq .module "cloudflare" -}}
{{- $cf := deepCopy (dig "cloudflare" dict $doc) -}}
{{- $serve := deepCopy (dig "serve" dict $cf) -}}
{{- $_ := set $serve "address" ($serve.address | default (printf ":%s" (include "sluis.moduleRPCPort" (dict "root" $root "module" "cloudflare")))) -}}
{{- $_ := set $serve "audience" ($serve.audience | default (include "sluis.moduleAudience" (dict "root" $root "module" "cloudflare"))) -}}
{{- $_ := set $cf "serve" $serve -}}
{{- $_ := set $doc "cloudflare" $cf -}}
{{- end -}}
{{- end -}}
{{- toYaml $doc -}}
{{- end }}

{{/* The document with its apiVersion; in documents mode the issuer's is `documents.service`, byte for byte. */}}
{{- define "sluis.moduleDocument" -}}
{{- if and (eq .module "issuer") (include "sluis.documentsMode" .root) -}}
{{- .root.Values.documents.service | trimSuffix "\n" -}}
{{- else -}}
{{- $kind := dict "issuer" "sluis" "github" "controller-github" "slack" "controller-slack" "google" "sluis-google" "cloudflare" "sluis" -}}
{{- include "sluis.document" (dict "kind" (get $kind .module) "doc" (fromYaml (include "sluis.moduleDocumentBody" .))) -}}
{{- end -}}
{{- end }}

{{/* "true" when the module gets a PodDisruptionBudget: on unless `podDisruptionBudget.enabled` says false. */}}
{{- define "sluis.modulePDBOn" -}}
{{- if dig "podDisruptionBudget" "enabled" true (get .root.Values.modules .module) }}true{{ end -}}
{{- end }}

{{/*
What the per-module model refuses. Takes the root.
*/}}
{{- define "sluis.moduleChecks" -}}
{{- $root := . -}}
{{- $docs := include "sluis.documentsMode" . -}}
{{- range $mod := list "github" "slack" "google" "cloudflare" -}}
{{- if and $docs (include "sluis.moduleOn" (dict "root" $root "module" $mod)) -}}
{{- fail (printf "modules.%s.enabled is true and documents.service is set: the chart writes `%s.remote` into the issuer's document, which it cannot do to a rendered one. Put the remote in the document, or use config" $mod $mod) -}}
{{- end -}}
{{- end -}}
{{- if not $docs -}}
{{- range $kind := list "github" "slack" -}}
{{- $own := dig "config" dict (get $root.Values.modules $kind) -}}
{{- $section := include "sluis.controllerOn" (dict "root" $root "kind" $kind) -}}
{{- if and $section (not (include "sluis.moduleOn" (dict "root" $root "module" $kind))) -}}
{{- fail (printf "config.controllers.%s is set, and modules.%s.enabled is not: the issuer does not run controllers, so nothing would. Enable the module, or remove the section" $kind $kind) -}}
{{- end -}}
{{- if and (include "sluis.moduleOn" (dict "root" $root "module" $kind)) (not $section) (not $own) -}}
{{- fail (printf "modules.%s.enabled is true and the module has no document section: set config.controllers.%s (an empty `{}` takes the defaults) or modules.%s.config. Without one the %s module would start with nothing to do" $kind $kind $kind $kind) -}}
{{- end -}}
{{- end -}}
{{- range $mod := list "google" "cloudflare" -}}
{{- if and (include "sluis.moduleOn" (dict "root" $root "module" $mod)) (not (dig "config" dict (get $root.Values.modules $mod))) -}}
{{- fail (printf "modules.%s.enabled is true and modules.%s.config is empty: the module reads its own document and has no other source, so write it there" $mod $mod) -}}
{{- end -}}
{{- end -}}
{{- range $callee := splitList "\n" (include "sluis.callees" $root) -}}
{{- if include "sluis.moduleOn" (dict "root" $root "module" $callee) -}}
{{- if dig $callee "remote" "" $root.Values.config -}}
{{- fail (printf "config.%s.remote is set and modules.%s.enabled is true: the chart writes the issuer's route to the module (url, audience and tokenFile), so say it once, by enabling the module" $callee $callee) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if and (include "sluis.moduleOn" (dict "root" $root "module" "cloudflare")) (dig "cloudflare" dict $root.Values.config) -}}
{{- fail "config.cloudflare is set and modules.cloudflare.enabled is true: the issuer's document then holds only `cloudflare.remote`, which the chart writes. The accounts and presets belong in modules.cloudflare.config.cloudflare" -}}
{{- end -}}
{{- end -}}
{{- range $mod := list "issuer" "github" "slack" "google" "cloudflare" -}}
{{- if include "sluis.moduleOn" (dict "root" $root "module" $mod) -}}
{{- $m := get $root.Values.modules $mod -}}
{{- $pdb := dig "podDisruptionBudget" dict $m -}}
{{- if and (not (kindIs "invalid" $pdb.minAvailable)) (not (kindIs "invalid" $pdb.maxUnavailable)) -}}
{{- fail (printf "modules.%s.podDisruptionBudget sets minAvailable and maxUnavailable: a budget says one of them" $mod) -}}
{{- end -}}
{{- $replicas := int (include "sluis.moduleReplicas" (dict "root" $root "module" $mod)) -}}
{{- if and (include "sluis.modulePDBOn" (dict "root" $root "module" $mod)) (not (include "sluis.moduleAutoscaled" (dict "root" $root "module" $mod))) (or (kindIs "float64" $pdb.minAvailable) (kindIs "int64" $pdb.minAvailable)) (ge (int $pdb.minAvailable) $replicas) -}}
{{- fail (printf "modules.%s.podDisruptionBudget.minAvailable is %v and the module runs %d replicas: no pod could ever be evicted, and a node drain would hang. Keep minAvailable below the replicas, or use maxUnavailable" $mod $pdb.minAvailable $replicas) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $hpa := dig "autoscaling" dict $root.Values.modules.issuer -}}
{{- if $hpa.enabled -}}
{{- if $root.Values.modules.issuer.replicas -}}
{{- fail "modules.issuer.autoscaling.enabled is true and modules.issuer.replicas is set: the HorizontalPodAutoscaler owns the replica count, so say the bounds in autoscaling.minReplicas and maxReplicas" -}}
{{- end -}}
{{- if lt (int $hpa.maxReplicas) (int $hpa.minReplicas) -}}
{{- fail (printf "modules.issuer.autoscaling.maxReplicas (%v) is below minReplicas (%v)" $hpa.maxReplicas $hpa.minReplicas) -}}
{{- end -}}
{{- if not (or $hpa.targetCPUUtilizationPercentage $hpa.targetMemoryUtilizationPercentage) -}}
{{- fail "modules.issuer.autoscaling.enabled is true and neither targetCPUUtilizationPercentage nor targetMemoryUtilizationPercentage is set: the autoscaler would have nothing to follow" -}}
{{- end -}}
{{- $requests := dig "requests" dict ($root.Values.modules.issuer.resources | default $root.Values.resources | default dict) -}}
{{- if and $hpa.targetCPUUtilizationPercentage (not $requests.cpu) -}}
{{- fail "modules.issuer.autoscaling.targetCPUUtilizationPercentage is a share of the CPU request, and resources.requests.cpu is not set (modules.issuer.resources, or resources)" -}}
{{- end -}}
{{- if and $hpa.targetMemoryUtilizationPercentage (not $requests.memory) -}}
{{- fail "modules.issuer.autoscaling.targetMemoryUtilizationPercentage is a share of the memory request, and resources.requests.memory is not set (modules.issuer.resources, or resources)" -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/* Replicas: `modules.<m>.replicas`; unset, `replicaCount` for the issuer and 1 for another module. */}}
{{- define "sluis.moduleReplicas" -}}
{{- (get .root.Values.modules .module).replicas | default (ternary .root.Values.replicaCount 1 (eq .module "issuer")) -}}
{{- end }}

{{/* "true" when an HorizontalPodAutoscaler scales the module: the issuer's only. */}}
{{- define "sluis.moduleAutoscaled" -}}
{{- if and (eq .module "issuer") (dig "autoscaling" "enabled" false .root.Values.modules.issuer) }}true{{ end -}}
{{- end }}

{{/*
One module's Deployment. Takes (dict "root" $ "module" "github").
*/}}
{{- define "sluis.moduleDeployment" -}}
{{- $root := .root -}}
{{- $mod := .module -}}
{{- $m := get $root.Values.modules $mod -}}
{{- $full := include "sluis.moduleFullname" . -}}
{{- $health := include "sluis.moduleHealthPort" . -}}
{{- $callee := include "sluis.isCallee" (dict "root" $root "module" $mod) -}}
{{- $replicas := include "sluis.moduleReplicas" . -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ $full }}
  labels:
    {{- include "sluis.labels" $root | nindent 4 }}
    app.kubernetes.io/component: {{ $mod }}
spec:
  {{- /* An HPA owns the replica count: a replicas field here would reset it on every upgrade. */}}
  {{- if not (include "sluis.moduleAutoscaled" .) }}
  replicas: {{ $replicas }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "sluis.selectorLabels" $root | nindent 6 }}
      app.kubernetes.io/component: {{ $mod }}
  template:
    metadata:
      labels:
        {{- include "sluis.selectorLabels" $root | nindent 8 }}
        app.kubernetes.io/component: {{ $mod }}
      annotations:
        # Each document is read once, at start: a change to it, or to the
        # policy, is a rollout.
        checksum/config: {{ include "sluis.moduleDocument" . | sha256sum }}
        checksum/policy: {{ include (print $root.Template.BasePath "/policy-configmap.yaml") $root | sha256sum }}
        {{- if and (eq $mod "issuer") $root.Values.signingKey.verifyOnly }}
        checksum/verify-keys: {{ include (print $root.Template.BasePath "/verify-keys.yaml") $root | sha256sum }}
        {{- end }}
        {{- with $root.Values.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
    spec:
      serviceAccountName: {{ include "sluis.moduleAccountName" . }}
      securityContext:
        runAsNonRoot: true
        {{- if include "sluis.secretFiles" $root }}
        runAsUser: 65532
        runAsGroup: 65532
        fsGroup: 65532
        {{- end }}
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: {{ $mod }}
          image: {{ include "sluis.moduleImage" . | quote }}
          imagePullPolicy: {{ $root.Values.image.pullPolicy }}
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: [ALL]
          ports:
            {{- if eq $mod "issuer" }}
            - name: issuer
              containerPort: {{ include "sluis.port" $root }}
            {{- end }}
            {{- if $callee }}
            - name: rpc
              containerPort: {{ include "sluis.moduleRPCPort" . }}
            {{- end }}
            - name: health
              containerPort: {{ $health }}
          args:
            - {{ $mod }}
            - --config=/etc/sluis/config.yaml
          {{- $otel := include "sluis.otelEnv" (dict "root" $root "service" (printf "sluis-%s" $mod)) }}
          {{- if or $root.Values.secretEnv $otel }}
          env:
            {{- range $root.Values.secretEnv }}
            - name: {{ .name }}
              valueFrom:
                secretKeyRef:
                  name: {{ .secretName }}
                  key: {{ .key }}
                  {{- if hasKey . "optional" }}
                  optional: {{ .optional }}
                  {{- end }}
            {{- end }}
            {{- with $otel }}
            {{- . | nindent 12 }}
            {{- end }}
          {{- end }}
          volumeMounts:
            - name: config
              mountPath: /etc/sluis/config.yaml
              subPath: config.yaml
              readOnly: true
            - name: policy
              mountPath: /var/run/access-issuer/policy
              readOnly: true
            - name: tmp
              mountPath: /tmp
            {{- include "sluis.auditMounts" (dict "root" $root "cfg" $root.Values.config) | nindent 12 }}
            {{- if eq $mod "issuer" }}
            {{- if not (include "sluis.remoteSigning" $root) }}
            - name: signing-key
              mountPath: /var/run/access-issuer/signing-key
              readOnly: true
            {{- range $root.Values.signingKey.additional }}
            {{- $name := include "sluis.additionalSigningKeyName" . }}
            - name: {{ $name }}
              mountPath: /var/run/access-issuer/{{ $name }}
              readOnly: true
            {{- end }}
            {{- end }}
            {{- if $root.Values.signingKey.verifyOnly }}
            - name: verify-keys
              mountPath: /var/run/access-issuer/verify-keys
              readOnly: true
            {{- end }}
            {{- range $callee := splitList "\n" (include "sluis.callees" $root) }}
            {{- if include "sluis.moduleOn" (dict "root" $root "module" $callee) }}
            # The token a call to {{ $callee }} carries, minted for its audience.
            - name: call-{{ $callee }}
              mountPath: {{ dir (include "sluis.callTokenFile" (dict "module" $callee)) }}
              readOnly: true
            {{- end }}
            {{- end }}
            {{- end }}
            {{- if include "sluis.hosts" (dict "root" $root "module" $mod "kind" "github") }}
            - name: github-token
              mountPath: /var/run/secrets/github-roster
              readOnly: true
            - name: github-apps
              mountPath: /var/run/github-roster/apps
              readOnly: true
            - name: github-records
              mountPath: /var/run/github-roster/records
              readOnly: true
            {{- end }}
            {{- if include "sluis.hosts" (dict "root" $root "module" $mod "kind" "slack") }}
            - name: slack-token
              mountPath: /var/run/secrets/slack-roster
              readOnly: true
            - name: slack-credentials
              mountPath: /var/run/slack-roster/credentials
              readOnly: true
            - name: slack-workspaces
              mountPath: /var/run/slack-roster/workspaces
              readOnly: true
            {{- end }}
            {{- with include "sluis.exportsMounts" $root }}
            {{- . | nindent 12 }}
            {{- end }}
            {{- if include "sluis.secretFiles" $root }}
            - name: secrets
              mountPath: /var/run/sluis/secrets
              readOnly: true
            {{- end }}
            {{- range $root.Values.secretMounts }}
            - name: secret-{{ .secretName }}
              mountPath: {{ .mountPath }}
              readOnly: true
            {{- end }}
          livenessProbe:
            httpGet: { path: /healthz, port: health }
          readinessProbe:
            httpGet: { path: /readyz, port: health }
            periodSeconds: 10
            timeoutSeconds: 3
            failureThreshold: 3
          {{- with ($m.resources | default $root.Values.resources) }}
          resources:
            {{- toYaml . | nindent 12 }}
          {{- end }}
      volumes:
        - name: config
          configMap:
            name: {{ $full }}-config
        - name: policy
          configMap:
            name: {{ include "sluis.fullname" $root }}-policy
        - name: tmp
          emptyDir: {}
        {{- include "sluis.auditVolumes" (dict "root" $root "cfg" $root.Values.config) | nindent 8 }}
        {{- if eq $mod "issuer" }}
        {{- if not (include "sluis.remoteSigning" $root) }}
        - name: signing-key
          secret:
            secretName: {{ include "sluis.signingKeySecret" $root }}
        {{- range $root.Values.signingKey.additional }}
        {{- $name := include "sluis.additionalSigningKeyName" . }}
        - name: {{ $name }}
          secret:
            secretName: {{ include "sluis.fullname" $root }}-{{ $name }}
        {{- end }}
        {{- end }}
        {{- if $root.Values.signingKey.verifyOnly }}
        - name: verify-keys
          configMap:
            name: {{ include "sluis.fullname" $root }}-verify-keys
        {{- end }}
        {{- range $callee := splitList "\n" (include "sluis.callees" $root) }}
        {{- if include "sluis.moduleOn" (dict "root" $root "module" $callee) }}
        - name: call-{{ $callee }}
          projected:
            sources:
              - serviceAccountToken:
                  audience: {{ include "sluis.moduleAudience" (dict "root" $root "module" $callee) }}
                  expirationSeconds: 3600
                  path: token
        {{- end }}
        {{- end }}
        {{- end }}
        {{- if include "sluis.hosts" (dict "root" $root "module" $mod "kind" "github") }}
        - name: github-token
          projected:
            sources:
              - serviceAccountToken:
                  audience: {{ include "sluis.exchangeAudience" $root }}
                  expirationSeconds: 3600
                  path: token
        - name: github-apps
          secret:
            secretName: {{ include "sluis.fullname" $root }}-github-apps
            optional: true
        - name: github-records
          configMap:
            name: {{ include "sluis.fullname" $root }}-github-orgs
            optional: true
        {{- end }}
        {{- if include "sluis.hosts" (dict "root" $root "module" $mod "kind" "slack") }}
        - name: slack-token
          projected:
            sources:
              - serviceAccountToken:
                  audience: {{ include "sluis.exchangeAudience" $root }}
                  expirationSeconds: 3600
                  path: token
        - name: slack-credentials
          secret:
            secretName: {{ include "sluis.fullname" $root }}-slack-credentials
            optional: true
        - name: slack-workspaces
          configMap:
            name: {{ include "sluis.fullname" $root }}-slack-workspaces
            optional: true
        {{- end }}
        {{- with include "sluis.exportsVolumes" $root }}
        {{- . | nindent 8 }}
        {{- end }}
        {{- if include "sluis.secretFiles" $root }}
        - name: secrets
          projected:
            defaultMode: 0440
            sources:
              {{- range $root.Values.secrets }}
              - secret:
                  name: {{ .secretName | quote }}
                  items:
                    - key: {{ .key | quote }}
                      path: {{ .name | quote }}
              {{- end }}
              {{- if not (include "sluis.documentsMode" $root) }}
              {{- range $id, $client := (include "sluis.declaredClients" $root | fromYaml) }}
              {{- if $client.secret }}
              - secret:
                  name: {{ $client.secret }}
                  items:
                    - key: {{ $client.secretKey | default "client-secret" }}
                      path: clients/{{ $id }}/secret
              {{- end }}
              {{- end }}
              {{- end }}
        {{- end }}
        {{- range $root.Values.secretMounts }}
        - name: secret-{{ .secretName }}
          secret:
            secretName: {{ .secretName }}
        {{- end }}
      {{- with $root.Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $root.Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
{{- end }}

{{/*
One module's ServiceAccount, from the release's `serviceAccount` and the module's own:
the same AWS identity, with the module's own role under irsa. Takes
(dict "root" $ "module" "github").
*/}}
{{- define "sluis.moduleServiceAccountObject" -}}
{{- $root := .root -}}
{{- /* The issuer is the release's own account that was: unless its module says otherwise, the messages name `serviceAccount`. */ -}}
{{- $key := printf "modules.%s.serviceAccount" .module -}}
{{- if and (eq .module "issuer") (not (or (dig "serviceAccount" "awsIdentity" "" $root.Values.modules.issuer) (dig "serviceAccount" "awsRoleArn" "" $root.Values.modules.issuer))) -}}
{{- $key = "serviceAccount" -}}
{{- end -}}
{{- $sa := deepCopy (dig "serviceAccount" dict (get $root.Values.modules .module)) -}}
{{- /* The issuer is the release's own account that was: it takes the release's role unless it names one. */ -}}
{{- if eq .module "issuer" -}}
{{- $_ := set $sa "awsRoleArn" ($sa.awsRoleArn | default $root.Values.serviceAccount.awsRoleArn) -}}
{{- end -}}
{{- $identity := $sa.awsIdentity | default $root.Values.serviceAccount.awsIdentity | default "pod-identity" -}}
{{- $annotations := deepCopy (mergeOverwrite (deepCopy ($root.Values.serviceAccount.annotations | default dict)) ($sa.annotations | default dict)) -}}
{{- if eq $identity "irsa" -}}
{{- $_ := required (printf "%s.awsIdentity is irsa, which annotates the account with the role: set %s.awsRoleArn (each module but the issuer has its own role)" $key $key) $sa.awsRoleArn -}}
{{- if hasKey $annotations "eks.amazonaws.com/role-arn" -}}
{{- fail (printf "%s.annotations names eks.amazonaws.com/role-arn and the identity is irsa: say the role once, in %s.awsRoleArn" $key $key) -}}
{{- end -}}
{{- $_ := set $annotations "eks.amazonaws.com/role-arn" $sa.awsRoleArn -}}
{{- else if $sa.awsRoleArn -}}
{{- fail (printf "%s.awsRoleArn is set and %s.awsIdentity is not irsa: EKS Pod Identity annotates nothing, so the ARN would be ignored. Set awsIdentity: irsa, or remove awsRoleArn" $key $key) -}}
{{- end -}}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "sluis.moduleAccountName" . }}
  labels:
    {{- include "sluis.labels" $root | nindent 4 }}
    app.kubernetes.io/component: {{ .module }}
  {{- with $annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}
