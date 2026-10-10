{{/*
The backup module's objects: a CronJob that backs up, one that prunes, and a
Job for an administrator that restores. They are not the service's pods and
carry no label the service's Service or NetworkPolicy selects: the name label
is "<name>-backup" or "<name>-restore". Each has an account of its own.
*/}}

{{/* The labels of one of the module's objects. Takes (dict "root" $ "component" "backup"). */}}
{{- define "sluis.moduleLabels" -}}
app.kubernetes.io/name: {{ printf "%s-%s" (include "sluis.name" .root) .component | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
app.kubernetes.io/component: {{ .component }}
app.kubernetes.io/part-of: {{ include "sluis.name" .root }}
{{- end }}

{{/* The account of "backup" or "restore". Takes (dict "root" $ "component" "backup"). */}}
{{- define "sluis.moduleServiceAccountName" -}}
{{- $v := index .root.Values .component -}}
{{- dig "serviceAccount" "name" "" $v | default (printf "%s-%s" (include "sluis.fullname" .root) .component | trunc 63 | trimSuffix "-") -}}
{{- end }}

{{/* The pod of one module object. Takes (dict "root" $ "component" "backup" "args" <list>). */}}
{{- define "sluis.modulePod" -}}
{{- $root := .root -}}
{{- $v := index $root.Values .component -}}
{{- $otel := include "sluis.otelEnv" (dict "root" $root "service" (printf "sluis-%s" .component)) -}}
metadata:
  labels:
    {{- include "sluis.moduleLabels" (dict "root" $root "component" .component) | nindent 4 }}
  annotations:
    checksum/config: {{ include (print $root.Template.BasePath "/backup-configmap.yaml") $root | sha256sum }}
    {{- with $v.podAnnotations }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
spec:
  restartPolicy: Never
  serviceAccountName: {{ include "sluis.moduleServiceAccountName" (dict "root" $root "component" .component) }}
  # The role's credentials are the platform's (Pod Identity, IRSA), which inject
  # their own token; the cluster API token is never used.
  automountServiceAccountToken: false
  securityContext:
    runAsNonRoot: true
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: {{ .component }}
      image: {{ include "sluis.image" (dict "root" $root "module" "backup") | quote }}
      imagePullPolicy: {{ $root.Values.image.pullPolicy }}
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities:
          drop: [ALL]
      args:
        {{- range .args }}
        - {{ . | quote }}
        {{- end }}
      {{- $secretEnv := ($root.Values.backup.secretEnv | default list) }}
      {{- if or $secretEnv $otel }}
      env:
        {{- range $secretEnv }}
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
        {{- . | nindent 8 }}
        {{- end }}
      {{- end }}
      volumeMounts:
        - name: config
          mountPath: /etc/sluis-backup/config.yaml
          subPath: config.yaml
          readOnly: true
        - name: tmp
          mountPath: /tmp
        {{- with include "sluis.auditMounts" (dict "root" $root "cfg" $root.Values.backup.config) }}
        {{- . | nindent 8 }}
        {{- end }}
      {{- with $v.resources }}
      resources:
        {{- toYaml . | nindent 8 }}
      {{- end }}
  volumes:
    - name: config
      configMap:
        name: {{ include "sluis.fullname" $root }}-backup-config
    - name: tmp
      emptyDir: {}
    {{- with include "sluis.auditVolumes" (dict "root" $root "cfg" $root.Values.backup.config) }}
    {{- . | nindent 4 }}
    {{- end }}
  {{- with $v.nodeSelector }}
  nodeSelector:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with $v.tolerations }}
  tolerations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}

{{/* The account of "backup" or "restore", as `serviceAccount` is. Takes (dict "root" $ "component" "backup"). */}}
{{- define "sluis.moduleServiceAccount" -}}
{{- $key := printf "%s.serviceAccount" .component -}}
{{- $sa := dig "serviceAccount" dict (index .root.Values .component) -}}
{{- $annotations := deepCopy ($sa.annotations | default dict) -}}
{{- if eq ($sa.awsIdentity | default "pod-identity") "irsa" -}}
{{- $_ := required (printf "%s.awsIdentity is irsa, which annotates the account with the role: set %s.awsRoleArn" $key $key) $sa.awsRoleArn -}}
{{- if hasKey $annotations "eks.amazonaws.com/role-arn" -}}
{{- fail (printf "%s.annotations names eks.amazonaws.com/role-arn and %s.awsIdentity is irsa: say the role once, in %s.awsRoleArn" $key $key $key) -}}
{{- end -}}
{{- $_ := set $annotations "eks.amazonaws.com/role-arn" $sa.awsRoleArn -}}
{{- else if $sa.awsRoleArn -}}
{{- fail (printf "%s.awsRoleArn is set and %s.awsIdentity is not irsa: EKS Pod Identity annotates nothing, so the ARN would be ignored. Set awsIdentity: irsa, or remove awsRoleArn" $key $key) -}}
{{- end -}}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "sluis.moduleServiceAccountName" . }}
  labels:
    {{- include "sluis.moduleLabels" . | nindent 4 }}
  {{- with $annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}
