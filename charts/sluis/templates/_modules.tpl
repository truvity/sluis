{{/*
The per-module workloads of `split: true` (ADR 0071, the v1.76 split). Nothing
here renders with `split: false`.
*/}}

{{- define "sluis.split" -}}
{{- if .Values.split }}yes{{ end -}}
{{- end }}

{{/* "true" when the module is enabled. Takes (dict "root" $ "module" "github"). */}}
{{- define "sluis.moduleOn" -}}
{{- if dig "enabled" false (get .root.Values.modules .module) }}true{{ end -}}
{{- end }}

{{/* <full name>-<module>: the Deployment, the Service, the document's ConfigMap (with -config) and the default account. */}}
{{- define "sluis.moduleFullname" -}}
{{- printf "%s-%s" (include "sluis.fullname" .root) .module | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "sluis.moduleAccountName" -}}
{{- dig "serviceAccount" "name" "" (get .root.Values.modules .module) | default (include "sluis.moduleFullname" .) -}}
{{- end }}

{{/* The modules other modules call, and which therefore answer on /rpc. */}}
{{- define "sluis.callees" -}}
github
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
  issuer      `config` without `controllers`, and `calls.modules.<m>` for each
              enabled module that answers calls
  github      config.controllers.github, with modules.github.config over it
  slack       likewise
  google      modules.google.config
  cloudflare  modules.cloudflare.config, and cloudflare.serve when unset
*/}}
{{- define "sluis.moduleDocumentBody" -}}
{{- $root := .root -}}
{{- $m := get $root.Values.modules .module -}}
{{- $doc := dict -}}
{{- if eq .module "issuer" -}}
{{- $doc = omit (deepCopy $root.Values.config) "controllers" -}}
{{- $mods := dict -}}
{{- range $callee := splitList "\n" (include "sluis.callees" $root) -}}
{{- if include "sluis.moduleOn" (dict "root" $root "module" $callee) -}}
{{- $_ := set $mods $callee (dict
      "url" (printf "http://%s.%s.svc:%d" (include "sluis.moduleFullname" (dict "root" $root "module" $callee)) $root.Release.Namespace (int (include "sluis.moduleRPCPort" (dict "root" $root "module" $callee))))
      "audience" (include "sluis.moduleAudience" (dict "root" $root "module" $callee))
      "tokenFile" (include "sluis.callTokenFile" (dict "module" $callee))) -}}
{{- end -}}
{{- end -}}
{{- if $mods -}}
{{- $calls := deepCopy (dig "calls" dict $doc) -}}
{{- $_ := set $calls "modules" (mergeOverwrite (deepCopy (dig "modules" dict $calls)) $mods) -}}
{{- $_ := set $doc "calls" $calls -}}
{{- end -}}
{{- else if or (eq .module "github") (eq .module "slack") -}}
{{- $doc = deepCopy (dig "controllers" .module dict $root.Values.config) -}}
{{- $_ := set $doc "consoleURL" (printf "http://%s.%s.svc:%s%s" (include "sluis.fullname" $root) $root.Release.Namespace (include "sluis.port" $root) $root.Values.console.mount) -}}
{{- $doc = mergeOverwrite $doc (deepCopy ($m.config | default dict)) -}}
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

{{/* The document with its apiVersion. */}}
{{- define "sluis.moduleDocument" -}}
{{- $kind := dict "issuer" "sluis" "github" "controller-github" "slack" "controller-slack" "google" "sluis-google" "cloudflare" "sluis" -}}
{{- include "sluis.document" (dict "kind" (get $kind .module) "doc" (fromYaml (include "sluis.moduleDocumentBody" .))) -}}
{{- end }}

{{/*
What `split` refuses. Takes the root.
*/}}
{{- define "sluis.splitChecks" -}}
{{- if include "sluis.split" . -}}
{{- if include "sluis.documentsMode" . -}}
{{- fail "split is true and documents.service is set: the chart writes `calls` into the issuer's document, which it cannot do to a rendered one. Use config, or keep split: false" -}}
{{- end -}}
{{- range $kind := list "github" "slack" -}}
{{- if and (include "sluis.controllerOn" (dict "root" $ "kind" $kind)) (not (include "sluis.moduleOn" (dict "root" $ "module" $kind))) -}}
{{- fail (printf "split is true and config.controllers.%s is set, and modules.%s.enabled is not: the issuer no longer runs the controller, so nothing would. Enable the module, or remove the section" $kind $kind) -}}
{{- end -}}
{{- end -}}
{{- end -}}
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
{{- $replicas := $m.replicas | default (ternary $root.Values.replicaCount 1 (eq $mod "issuer")) -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ $full }}
  labels:
    {{- include "sluis.labels" $root | nindent 4 }}
    app.kubernetes.io/component: {{ $mod }}
spec:
  replicas: {{ $replicas }}
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
            {{- if eq $mod "github" }}
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
            {{- if eq $mod "slack" }}
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
          {{- with $m.resources }}
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
        {{- if eq $mod "github" }}
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
        {{- if eq $mod "slack" }}
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
One module's ServiceAccount, as `serviceAccount` is for the single Deployment:
the same AWS identity, with the module's own role under irsa. Takes
(dict "root" $ "module" "github").
*/}}
{{- define "sluis.moduleServiceAccountObject" -}}
{{- $root := .root -}}
{{- $key := printf "modules.%s.serviceAccount" .module -}}
{{- $sa := dig "serviceAccount" dict (get $root.Values.modules .module) -}}
{{- $identity := $sa.awsIdentity | default $root.Values.serviceAccount.awsIdentity | default "pod-identity" -}}
{{- $annotations := deepCopy (mergeOverwrite (deepCopy ($root.Values.serviceAccount.annotations | default dict)) ($sa.annotations | default dict)) -}}
{{- if eq $identity "irsa" -}}
{{- $_ := required (printf "%s.awsIdentity is irsa, which annotates the account with the role: set %s.awsRoleArn (each module has its own role)" $key $key) $sa.awsRoleArn -}}
{{- if hasKey $annotations "eks.amazonaws.com/role-arn" -}}
{{- fail (printf "%s.annotations names eks.amazonaws.com/role-arn and the identity is irsa: say the role once, in %s.awsRoleArn" $key $key) -}}
{{- end -}}
{{- $_ := set $annotations "eks.amazonaws.com/role-arn" $sa.awsRoleArn -}}
{{- else if $sa.awsRoleArn -}}
{{- fail (printf "%s.awsRoleArn is set and the identity is not irsa: EKS Pod Identity annotates nothing, so the ARN would be ignored. Set %s.awsIdentity: irsa, or remove awsRoleArn" $key $key) -}}
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

{{/* The account the issuer runs as: the release's own, or with `split` the issuer module's. */}}
{{- define "sluis.issuerAccount" -}}
{{- if include "sluis.split" . -}}
{{- include "sluis.moduleAccountName" (dict "root" . "module" "issuer") -}}
{{- else -}}
{{- include "sluis.serviceAccountName" . -}}
{{- end -}}
{{- end }}
