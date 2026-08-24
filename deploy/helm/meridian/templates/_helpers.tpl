{{- define "meridian.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "meridian.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "meridian.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "meridian.namespace" -}}
{{- default .Release.Namespace .Values.namespace.name -}}
{{- end -}}

{{- define "meridian.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "meridian.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- required "serviceAccount.name is required when create=false" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "meridian.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (required "image.tag or image.digest is required" .Values.image.tag) -}}
{{- end -}}
{{- end -}}

{{- define "meridian.capsuleImage" -}}
{{- if .Values.capsuleImage.digest -}}
{{- printf "%s@%s" .Values.capsuleImage.repository .Values.capsuleImage.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.capsuleImage.repository (required "capsuleImage.tag or capsuleImage.digest is required" .Values.capsuleImage.tag) -}}
{{- end -}}
{{- end -}}

{{- define "meridian.labels" -}}
app.kubernetes.io/name: {{ include "meridian.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}
