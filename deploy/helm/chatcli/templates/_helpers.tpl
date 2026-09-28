{{/*
Expand the name of the chart.
*/}}
{{- define "chatcli.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "chatcli.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "chatcli.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "chatcli.labels" -}}
helm.sh/chart: {{ include "chatcli.chart" . }}
{{ include "chatcli.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "chatcli.selectorLabels" -}}
app.kubernetes.io/name: {{ include "chatcli.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "chatcli.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "chatcli.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Secret name for provider credentials
*/}}
{{- define "chatcli.secretName" -}}
{{- if .Values.secrets.existingSecret }}
{{- .Values.secrets.existingSecret }}
{{- else }}
{{- include "chatcli.fullname" . }}
{{- end }}
{{- end }}

{{/*
Secret that carries CHATCLI_SERVER_TOKEN when server.token is set.
The chart Secret holds it when the chart manages secrets; with
secrets.existingSecret the chart cannot write into the user's Secret, so
the token goes into a small dedicated Secret instead. Either way the value
reaches the pod through a secretKeyRef, never through the container args.
*/}}
{{- define "chatcli.tokenSecretName" -}}
{{- if .Values.secrets.existingSecret }}
{{- printf "%s-server-token" (include "chatcli.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- include "chatcli.fullname" . }}
{{- end }}
{{- end }}

{{/*
The MCP config is mounted when MCP is enabled and there is something to
mount: inline servers (rendered into the chart ConfigMap) or an existing
ConfigMap carrying mcp_servers.json.
*/}}
{{- define "chatcli.mcpConfigEnabled" -}}
{{- if and .Values.mcp.enabled (or .Values.mcp.servers .Values.mcp.existingConfigMap) }}true{{- end }}
{{- end }}

{{/*
Process probe for liveness and startup. GET /healthz on the metrics
listener answers 200 as soon as the process is up and needs neither TLS nor
a credential, and works for every server image version and with or without
TLS (a kubelet gRPC probe cannot speak TLS). With metrics disabled it falls
back to a TCP connect on the gRPC port.
*/}}
{{- define "chatcli.processProbe" -}}
{{- if .Values.server.metricsPort }}
httpGet:
  path: /healthz
  port: metrics
{{- else }}
tcpSocket:
  port: grpc
{{- end }}
{{- end }}
