{{/*
The install preset: how much of the audit system this release provisions.
`operational` is the writer, the archive, deduplication and the intake;
`standard` adds the notary and its seal key (and the alarms, which are the
`renders: alerts` release); `attested` adds compliance Object Lock and the
pseudonym keys, which are the store's and the key provider's to provide.

It is derived, with the rule profile.Deployment.Derive has in Go: the highest
`min_preset` of the framework profiles the `profiles` compose, from the table
_presets.tpl is generated as. `preset` may name a stronger one and is refused
when weaker, naming the profile that needs more. Nothing chosen is operational
(audit.profiles then supplies a `history` profile).
Renders the effective preset's name.
*/}}
{{- define "audit.preset" -}}
{{- $min := fromYaml (include "audit.frameworkPresets" .) -}}
{{- $rank := dict "operational" 0 "standard" 1 "attested" 2 -}}
{{- $profiles := fromYaml (include "audit.profiles" .) -}}
{{- $derived := "operational" -}}
{{- range $name, $profile := $profiles -}}
  {{- range $fw := ($profile.frameworks | default list) -}}
    {{- if not (hasKey $min $fw) -}}
    {{- fail (printf "audit: profile %s composes %q, which is not a framework profile this chart knows (%s)." $name $fw (keys $min | sortAlpha | join ", ")) -}}
    {{- end -}}
    {{- if gt (index $rank (index $min $fw)) (index $rank $derived) -}}
    {{- $derived = index $min $fw -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- $explicit := .Values.preset | default "" -}}
{{- if and $explicit (not (hasKey $rank $explicit)) -}}
{{- fail (printf "audit: `preset` is operational, standard or attested, not %q." $explicit) -}}
{{- end -}}
{{- if and $explicit (lt (index $rank $explicit) (index $rank $derived)) -}}
  {{- $needs := list -}}
  {{- range $name, $profile := $profiles -}}
    {{- range $fw := ($profile.frameworks | default list) -}}
      {{- if lt (index $rank $explicit) (index $rank (index $min $fw)) -}}
      {{- $needs = append $needs (printf "profile %s (framework profile %s) needs %s" $name $fw (index $min $fw)) -}}
      {{- end -}}
    {{- end -}}
  {{- end -}}
  {{- fail (printf "audit: preset %s is weaker than the profiles need: %s. Set `preset` to %s or stronger, or compose the profile from framework profiles that need less." $explicit (join "; " (sortAlpha $needs)) $derived) -}}
{{- end -}}
{{- ternary $explicit $derived (ne $explicit "") -}}
{{- end -}}
