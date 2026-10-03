{{- define "theophylax.name" -}}{{ .Chart.Name | trunc 63 | trimSuffix "-" }}{{- end -}}
{{- define "theophylax.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}{{ .Release.Name | trunc 63 | trimSuffix "-" }}{{- else -}}{{ printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}{{- end -}}
{{- end -}}
{{- define "theophylax.labels" -}}
app.kubernetes.io/name: {{ include "theophylax.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}
{{- define "theophylax.selector" -}}
app.kubernetes.io/name: {{ include "theophylax.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "theophylax.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}{{ default (include "theophylax.fullname" .) .Values.serviceAccount.name }}{{- else -}}{{ default "default" .Values.serviceAccount.name }}{{- end -}}
{{- end -}}
