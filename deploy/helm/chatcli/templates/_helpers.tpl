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

{{/*
"true" when the sessions volume attaches to one node at a time:
persistence on, and neither ReadWriteMany nor ReadOnlyMany requested.
*/}}
{{- define "chatcli.sessionsVolumeSingleNode" -}}
{{- if .Values.persistence.enabled }}
{{- $modes := .Values.persistence.accessModes | default (list "ReadWriteOnce") }}
{{- if not (or (has "ReadWriteMany" $modes) (has "ReadOnlyMany" $modes)) }}true{{- end }}
{{- end }}
{{- end }}

{{/*
Deployment update strategy. An explicit .Values.strategy is rendered as
written. Otherwise a single-node sessions volume stops the old pod before
the new one starts: with the API server default (RollingUpdate, maxSurge
25% = 1 pod, maxUnavailable 25% = 0 pods) the new pod, when scheduled on
another node, waits forever on Multi-Attach while the old pod, stopped only
once the new one is Ready, holds the volume. Every other shape keeps the
API server default by rendering nothing, as before.

The stop-first rollout is RollingUpdate with maxSurge 0 and
maxUnavailable 1 rather than type Recreate: switching a live Deployment to
Recreate has to remove its defaulted rollingUpdate block, and Helm 4's
server-side apply cannot remove a field no manager owns, so the upgrade
of every existing release installed by Helm 4 would fail with
"spec.strategy.rollingUpdate: Forbidden". These two values apply under
client-side and server-side apply alike.

An explicit type Recreate is rendered with rollingUpdate: null, which lets
Helm's client-side three-way merge drop the defaulted block.
*/}}
{{- define "chatcli.strategy" -}}
{{- $strategy := dict }}
{{- if .Values.strategy }}
{{- $strategy = deepCopy .Values.strategy }}
{{- else if include "chatcli.sessionsVolumeSingleNode" . }}
{{- $strategy = dict "type" "RollingUpdate" "rollingUpdate" (dict "maxSurge" 0 "maxUnavailable" 1) }}
{{- end }}
{{- if $strategy }}
{{- if and (eq ($strategy.type | default "") "Recreate") (not (hasKey $strategy "rollingUpdate")) }}
{{- $_ := set $strategy "rollingUpdate" nil }}
{{- end }}
strategy:
  {{- toYaml $strategy | nindent 2 }}
{{- end }}
{{- end }}

{{/*
Render-time checks of value combinations that cannot work.
*/}}
{{- define "chatcli.validate" -}}
{{- if and (include "chatcli.sessionsVolumeSingleNode" .) (has "ReadWriteOncePod" (.Values.persistence.accessModes | default list)) }}
{{- $replicas := int .Values.replicaCount }}
{{- if .Values.autoscaling.enabled }}
{{- $replicas = max $replicas (int .Values.autoscaling.maxReplicas) }}
{{- end }}
{{- if gt (int $replicas) 1 }}
{{- fail "persistence.accessModes ReadWriteOncePod admits a single pod, but replicaCount or autoscaling.maxReplicas asks for more than one: every extra replica would stay Pending. Run one replica, or use ReadWriteMany for several" }}
{{- end }}
{{- end }}
{{- if and .Values.security.jwtSecret .Values.security.jwtSecretRef }}
{{- fail "security.jwtSecret and security.jwtSecretRef both set CHATCLI_JWT_SECRET: set only one (jwtSecretRef keeps the secret out of the values)" }}
{{- end }}
{{- if and .Values.security.jwtPublicKey .Values.security.jwtPublicKeyRef }}
{{- fail "security.jwtPublicKey and security.jwtPublicKeyRef both set CHATCLI_JWT_PUBLIC_KEY: set only one" }}
{{- end }}
{{- $logEnvFromExtra := false }}
{{- range .Values.extraEnv }}
{{- if has .name (list "CHATCLI_LOG_MAX_SIZE_MB" "CHATCLI_LOG_MAX_BACKUPS") }}
{{- $logEnvFromExtra = true }}
{{- end }}
{{- end }}
{{- /* extraEnv owning the rotation variables is the explicit escape hatch: not checked. */}}
{{- if and .Values.securityContext .Values.securityContext.readOnlyRootFilesystem (not $logEnvFromExtra) }}
{{- /* A null field leaves the server default in force: 100 MB, 3 backups. */}}
{{- $logging := include "chatcli.loggingValues" . | fromJson }}
{{- $size := 100 }}
{{- if not (kindIs "invalid" $logging.maxSizeMB) }}
{{- $size = int $logging.maxSizeMB }}
{{- end }}
{{- $backups := 3 }}
{{- if not (kindIs "invalid" $logging.maxBackups) }}
{{- $backups = int $logging.maxBackups }}
{{- end }}
{{- $total := mul $size (add $backups 1) }}
{{- if gt (int $total) 100 }}
{{- fail (printf "logging.maxSizeMB x (logging.maxBackups + 1) = %d MB of log on the 200Mi data emptyDir that holds /home/chatcli/.chatcli under the read-only root filesystem; keep it within 100 MB so the volume cannot pass its sizeLimit and get the pod evicted" (int $total)) }}
{{- end }}
{{- end }}
{{- end }}

{{/*
The effective logging block, as JSON. An upgrade with plain --reuse-values
carries no logging key at all (it skips the defaults of keys added by newer
chart versions); it gets the chart defaults here instead of the server's
100 MB x 4, which do not fit the data emptyDir. A logging key that is
present is used as it is, null fields (removed by Helm) included.
*/}}
{{- define "chatcli.loggingValues" -}}
{{- if hasKey .Values "logging" }}
{{- .Values.logging | default dict | toJson }}
{{- else }}
{{- dict "maxSizeMB" 20 "maxBackups" 3 "maxAgeDays" 28 "compress" true | toJson }}
{{- end }}
{{- end }}

{{/*
CHATCLI_LOG_* entries from .Values.logging, as an env list. A null field
renders nothing (server default), and a name extraEnv also sets is left to
extraEnv, so the container never carries it twice.
*/}}
{{- define "chatcli.loggingEnv" -}}
{{- $taken := dict }}
{{- range .Values.extraEnv }}
{{- $_ := set $taken .name true }}
{{- end }}
{{- $env := list }}
{{- $logging := include "chatcli.loggingValues" . | fromJson }}
{{- range $field, $name := dict "maxSizeMB" "CHATCLI_LOG_MAX_SIZE_MB" "maxBackups" "CHATCLI_LOG_MAX_BACKUPS" "maxAgeDays" "CHATCLI_LOG_MAX_AGE_DAYS" "compress" "CHATCLI_LOG_COMPRESS" }}
{{- $v := index $logging $field }}
{{- if and (not (kindIs "invalid" $v)) (not (hasKey $taken $name)) }}
{{- $env = append $env (dict "name" $name "value" (toString $v)) }}
{{- end }}
{{- end }}
{{- with $env }}
{{- toYaml . }}
{{- end }}
{{- end }}

{{/*
Name of the ConfigMap mounted for agents, skills or bootstrap (list . "agents"),
or nothing when there is none to mount: the feature disabled, or enabled
with neither inline definitions (which the chart renders into
<fullname>-<feature>) nor an existingConfigMap. Mounting the chart's
ConfigMap when it was never rendered left the pod in ContainerCreating;
without the mount the server simply finds no files in that directory.
*/}}
{{- define "chatcli.configMapName" -}}
{{- $root := index . 0 }}
{{- $feature := index . 1 }}
{{- $v := index $root.Values $feature }}
{{- if $v.enabled }}
{{- if $v.existingConfigMap }}
{{- $v.existingConfigMap }}
{{- else if $v.definitions }}
{{- printf "%s-%s" (include "chatcli.fullname" $root) $feature }}
{{- end }}
{{- end }}
{{- end }}
