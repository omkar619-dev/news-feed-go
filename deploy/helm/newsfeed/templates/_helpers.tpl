{{/*
Reusable template snippets. Call them with {{ include "newsfeed.fullname" . }}.
Defining names/labels ONCE here keeps every resource consistent.
*/}}

{{- define "newsfeed.name" -}}
{{- .Chart.Name -}}
{{- end -}}

{{/* A release-scoped name, e.g. "myrelease-newsfeed", so two installs don't collide. */}}
{{- define "newsfeed.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* Common labels stamped on every resource (good for querying/grouping). */}}
{{- define "newsfeed.labels" -}}
app.kubernetes.io/name: {{ include "newsfeed.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{/*
selectorLabels: the SUBSET used to match pods to a Deployment/Service.
Takes the component name as an argument so each component (api/worker/postgres/…)
selects only ITS OWN pods. Call: {{ include "newsfeed.selectorLabels" (dict "ctx" . "component" "api") }}
*/}}
{{- define "newsfeed.selectorLabels" -}}
app.kubernetes.io/name: {{ include "newsfeed.name" .ctx }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}
