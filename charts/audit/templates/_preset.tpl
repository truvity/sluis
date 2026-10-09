{{/*
The install presets: how much of the audit system each profile's copies are
kept with. `operational` is the writer, the archive, deduplication and the
intake; `standard` adds the notary and its seal key (and the alarms, which are
the `renders: alerts` release); `attested` adds compliance Object Lock on the
preset's bucket (S3 only) and the pseudonym keys, which are the store's and the
key provider's to provide.

Storage is configured per preset (`presets`), and a profile is kept under the
preset it needs: the highest `min_preset` of the framework profiles it
composes, from the table _presets.tpl is generated as, or the stronger one the
profile asks for with its own `preset` (asking for less is refused). That
preset must be configured. This is the rule profile.Deployment.CheckStorage has
in Go.
*/}}

{{/* Each profile's preset, as YAML (profile: preset). Refuses a framework
profile the chart does not know, a `preset` that is not one of the three, and a
profile `preset` weaker than its framework profiles need. */}}
{{- define "audit.profilePresets" -}}
{{- $min := fromYaml (include "audit.frameworkPresets" .) -}}
{{- $rank := dict "operational" 0 "standard" 1 "attested" 2 -}}
{{- $out := dict -}}
{{- range $name, $profile := (include "audit.profiles" . | fromYaml) -}}
  {{- $need := "operational" -}}
  {{- $by := list -}}
  {{- range $fw := ($profile.frameworks | default list) -}}
    {{- if not (hasKey $min $fw) -}}
    {{- fail (printf "audit: profile %s composes %q, which is not a framework profile this chart knows (%s)." $name $fw (keys $min | sortAlpha | join ", ")) -}}
    {{- end -}}
    {{- if gt (index $rank (index $min $fw)) (index $rank $need) -}}
    {{- $need = index $min $fw -}}
    {{- $by = list $fw -}}
    {{- else if and (eq (index $min $fw) $need) (gt (index $rank $need) 0) -}}
    {{- $by = append $by $fw -}}
    {{- end -}}
  {{- end -}}
  {{- $explicit := $profile.preset | default "" -}}
  {{- if and $explicit (not (hasKey $rank $explicit)) -}}
  {{- fail (printf "audit: profile %s: preset is operational, standard or attested, not %q." $name $explicit) -}}
  {{- end -}}
  {{- if and $explicit (lt (index $rank $explicit) (index $rank $need)) -}}
  {{- fail (printf "audit: profile %s asks for the %s preset and its framework profiles %s need %s. Ask for %s or stronger, or compose the profile from framework profiles that need less." $name $explicit (join ", " $by) $need $need) -}}
  {{- end -}}
  {{- $_ := set $out $name (ternary $explicit $need (ne $explicit "")) -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{/* What the configured presets provision, as YAML: any configured preset that
has a feature turns it on for the installation. Refuses a name that is not a
preset. */}}
{{- define "audit.features" -}}
{{- $rank := dict "operational" 0 "standard" 1 "attested" 2 -}}
{{- $f := dict "notary" false "alarms" false "objectLock" false "pseudonymKeys" false -}}
{{- range $name, $_ := (.Values.presets | default dict) -}}
  {{- if not (hasKey $rank $name) -}}
  {{- fail (printf "audit: `presets` is keyed by operational, standard or attested, not %q." $name) -}}
  {{- end -}}
  {{- if ge (index $rank $name) 1 -}}{{- $_ := set $f "notary" true -}}{{- $_ := set $f "alarms" true -}}{{- end -}}
  {{- if ge (index $rank $name) 2 -}}{{- $_ := set $f "objectLock" true -}}{{- $_ := set $f "pseudonymKeys" true -}}{{- end -}}
{{- end -}}
{{- toYaml $f -}}
{{- end -}}

{{/* The storage of the presets, held to the rules the deployment document is
held to in Go (profile.Deployment.CheckStorage and PresetStorage.check). */}}
{{- define "audit.storageChecks" -}}
{{- $presets := .Values.presets | default dict -}}
{{- $configured := keys $presets | sortAlpha | join ", " | default "none" -}}
{{- if not $presets -}}
{{- fail "audit: `presets` is empty: name the storage of each install preset the profiles use (presets: {standard: {bucket: ..., prefix: ...}})." -}}
{{- end -}}
{{- range $name, $s := $presets -}}
  {{- $s = $s | default dict -}}
  {{- if not $s.bucket -}}
  {{- fail (printf "audit: presets.%s: bucket is required." $name) -}}
  {{- end -}}
  {{- if and $s.key_alias (not (regexMatch "^alias/[A-Za-z0-9/_-]+$" $s.key_alias)) -}}
  {{- fail (printf "audit: presets.%s.key_alias %q must be an alias (alias/<name>), never a key id or ARN." $name $s.key_alias) -}}
  {{- end -}}
  {{- if and $s.prefix (or (hasPrefix "/" $s.prefix) (not (hasSuffix "/" $s.prefix))) -}}
  {{- fail (printf "audit: presets.%s.prefix %q is a path ending in a slash and not starting with one (%s/)." $name $s.prefix $name) -}}
  {{- end -}}
  {{- if and $s.credentials_ref (or $s.credentials $s.credentials_preset) -}}
  {{- fail (printf "audit: presets.%s names credentials_ref and credentials or credentials_preset: one source of credentials, not two." $name) -}}
  {{- end -}}
  {{- if and $s.credentials_preset (not $.Values.acknowledgeMinterCustody) -}}
  {{- fail (printf "audit: presets.%s names credentials_preset, which puts the Cloudflare minter in every process that reads the archive; a minter can mint any right its creating user holds. Use credentials_ref (the R2 credential sluis already rotates), or set acknowledgeMinterCustody: true to accept the custody." $name) -}}
  {{- end -}}
  {{- if $s.endpoint -}}
    {{- if not (regexMatch "^https?://[^/?#[:space:]]+" $s.endpoint) -}}
    {{- fail (printf "audit: presets.%s.endpoint %q is not an http(s) URL." $name $s.endpoint) -}}
    {{- end -}}
    {{- if $s.key_alias -}}
    {{- fail (printf "audit: presets.%s names key_alias %s and its store is the S3-compatible endpoint %s, which is not encrypted under a KMS key of the account: leave key_alias out, or keep the preset on AWS S3." $name $s.key_alias $s.endpoint) -}}
    {{- end -}}
    {{- if eq $name "attested" -}}
    {{- fail (printf "audit: presets.attested is on the S3-compatible endpoint %s, which has no Object Lock: the attested preset keeps its objects under compliance Object Lock, which is S3 only. Keep it on AWS S3." $s.endpoint) -}}
    {{- end -}}
  {{- else -}}
    {{- if $s.credentials -}}
    {{- fail (printf "audit: presets.%s.credentials are static credentials for a store at an endpoint; on AWS the workload's identity is the credential (set endpoint, or leave credentials out)." $name) -}}
    {{- end -}}
    {{- if $s.path_style -}}
    {{- fail (printf "audit: presets.%s.path_style is for a store at an endpoint." $name) -}}
    {{- end -}}
    {{- if $s.credentials_ref -}}
    {{- fail (printf "audit: presets.%s.credentials_ref names R2 credentials for a store at an endpoint; on AWS the workload's identity is the credential (set endpoint, or leave credentials_ref out)." $name) -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- range $profile, $need := (include "audit.profilePresets" . | fromYaml) -}}
  {{- if not (hasKey $presets $need) -}}
  {{- fail (printf "audit: profile %s is kept under the %s preset and `presets` configures only %s: configure presets.%s, or set the profile's `preset` to one that is configured." $profile $need $configured $need) -}}
  {{- end -}}
{{- end -}}
{{- end -}}
