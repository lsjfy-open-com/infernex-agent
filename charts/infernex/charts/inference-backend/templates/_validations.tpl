{{/*
Validate required configuration values
*/}}
{{- define "vllm.validate" -}}
{{- $errors := list }}

{{/* Validate global configuration */}}
{{- if not .Values.global }}
{{- $errors = append $errors "global is required" }}
{{- else }}
{{- if not .Values.global.modelName }}
{{- $errors = append $errors "global.modelName is required" }}
{{- end }}
{{- if not .Values.global.cachePath }}
{{- $errors = append $errors "global.cachePath is required" }}
{{- end }}
{{- end }}

{{/* Validate inference engine image configuration */}}
{{- if not .Values.images.inferenceEngine }}
{{- $errors = append $errors "images.inferenceEngine is required" }}
{{- else }}
{{- if not .Values.images.inferenceEngine.repository }}
{{- $errors = append $errors "images.inferenceEngine.repository is required" }}
{{- end }}
{{- if not .Values.images.inferenceEngine.tag }}
{{- $errors = append $errors "images.inferenceEngine.tag is required" }}
{{- end }}
{{- end }}

{{/* Validate proxy server image configuration */}}
{{- if not .Values.images.proxyServer }}
{{- $errors = append $errors "images.proxyServer is required" }}
{{- else }}
{{- if not .Values.images.proxyServer.repository }}
{{- $errors = append $errors "images.proxyServer.repository is required" }}
{{- end }}
{{- end }}

{{/* Validate redis image configuration */}}
{{- if .Values.images.redis }}
{{- if not .Values.images.redis.repository }}
{{- $errors = append $errors "images.redis.repository is required when images.redis is defined" }}
{{- end }}
{{- if not .Values.images.redis.tag }}
{{- $errors = append $errors "images.redis.tag is required when images.redis is defined" }}
{{- end }}
{{- end }}

{{/* Validate yaml tool image configuration */}}
{{- if not .Values.images.yamlTool }}
{{- $errors = append $errors "images.yamlTool is required" }}
{{- else }}
{{- if not .Values.images.yamlTool.repository }}
{{- $errors = append $errors "images.yamlTool.repository is required" }}
{{- end }}
{{- if not .Values.images.yamlTool.tag }}
{{- $errors = append $errors "images.yamlTool.tag is required" }}
{{- end }}
{{- end }}

{{/* Validate services configuration */}}
{{- $hasEnabledService := false }}
{{- range .Values.services }}
{{- if .enabled }}
{{- $hasEnabledService = true }}
{{- end }}
{{/* Validate service mode must be 'pd' or 'aggregated' */}}
{{- if .mode }}
{{- if and (ne .mode "pd") (ne .mode "aggregated") }}
{{- $errors = append $errors (printf "services[%s].mode must be 'pd' or 'aggregated', got '%s'" (.name | default "unknown") .mode) }}
{{- end }}
{{- else }}
{{- $errors = append $errors (printf "services[%s].mode is required" (.name | default "unknown")) }}
{{- end }}
{{/* kvTransferConfig is required for pd services */}}
{{- if and .enabled (eq .mode "pd") (not .kvTransferConfig) }}
{{- $errors = append $errors (printf "services[%s].kvTransferConfig is required when mode is 'pd'" (.name | default "unknown")) }}
{{- end }}
{{/* dataParallelSize must be divisible by dataParallelSizeLocal (default 1 = one global rank span per Pod). */}}
{{- if and .enabled (eq .mode "pd") }}
{{- $dps := int (.pd.prefill.dataParallelSize | default 1) }}
{{- $dpl := int (.pd.prefill.dataParallelSizeLocal | default 1) }}
{{- if lt $dpl 1 }}
{{- $errors = append $errors (printf "services[%s].pd.prefill.dataParallelSizeLocal must be >= 1" (.name | default "unknown")) }}
{{- else if and (gt $dps 0) (ne (mod $dps $dpl) 0) }}
{{- $errors = append $errors (printf "services[%s].pd.prefill.dataParallelSize (%d) must be divisible by dataParallelSizeLocal (%d)" (.name | default "unknown") $dps $dpl) }}
{{- end }}
{{- $dpsd := int (.pd.decode.dataParallelSize | default 1) }}
{{- $dpld := int (.pd.decode.dataParallelSizeLocal | default 1) }}
{{- if lt $dpld 1 }}
{{- $errors = append $errors (printf "services[%s].pd.decode.dataParallelSizeLocal must be >= 1" (.name | default "unknown")) }}
{{- else if and (gt $dpsd 0) (ne (mod $dpsd $dpld) 0) }}
{{- $errors = append $errors (printf "services[%s].pd.decode.dataParallelSize (%d) must be divisible by dataParallelSizeLocal (%d)" (.name | default "unknown") $dpsd $dpld) }}
{{- end }}
{{- end }}
{{/* multi-DP: dataParallelRpcPort is required between ranks; dataParallelAddress comes from LWS env (not user-set). */}}
{{- if and .enabled (eq .mode "pd") (gt (int (.pd.prefill.dataParallelSize | default 1)) 1) }}
{{- if not .pd.prefill.dataParallelRpcPort }}
{{- $errors = append $errors (printf "services[%s].pd.prefill.dataParallelRpcPort is required when pd.prefill.dataParallelSize>1 " (.name | default "unknown")) }}
{{- end }}
{{- end }}
{{- if and .enabled (eq .mode "pd") (gt (int (.pd.decode.dataParallelSize | default 1)) 1) }}
{{- if not .pd.decode.dataParallelRpcPort }}
{{- $errors = append $errors (printf "services[%s].pd.decode.dataParallelRpcPort is required when pd.decode.dataParallelSize>1" (.name | default "unknown")) }}
{{- end }}
{{- end }}
{{- if and .enabled (eq .mode "aggregated") }}
{{- $dpsa := int (.aggregated.dataParallelSize | default 1) }}
{{- $dpla := int (.aggregated.dataParallelSizeLocal | default 1) }}
{{- if lt $dpla 1 }}
{{- $errors = append $errors (printf "services[%s].aggregated.dataParallelSizeLocal must be >= 1" (.name | default "unknown")) }}
{{- else if and (gt $dpsa 0) (ne (mod $dpsa $dpla) 0) }}
{{- $errors = append $errors (printf "services[%s].aggregated.dataParallelSize (%d) must be divisible by dataParallelSizeLocal (%d) for LWS sizing" (.name | default "unknown") $dpsa $dpla) }}
{{- end }}
{{- end }}
{{- if and .enabled (eq .mode "aggregated") (gt (int (.aggregated.dataParallelSize | default 1)) 1) }}
{{- if not .aggregated.dataParallelRpcPort }}
{{- $errors = append $errors (printf "services[%s].aggregated.dataParallelRpcPort is required when aggregated.dataParallelSize>1 (LWS)" (.name | default "unknown")) }}
{{- end }}
{{- end }}
{{/* maxNumBatchedTokens must be a multiple of tensorParallelSize when both are set (vLLM / TP scheduling). */}}
{{- if and .enabled (eq .mode "pd") }}
{{- $tpP := int (.pd.prefill.tensorParallelSize | default 1) }}
{{- if and (hasKey .pd.prefill "maxNumBatchedTokens") (gt $tpP 0) }}
{{- $mbp := int .pd.prefill.maxNumBatchedTokens }}
{{- if ne (mod $mbp $tpP) 0 }}
{{- $errors = append $errors (printf "services[%s].pd.prefill.maxNumBatchedTokens (%d) must be a multiple of tensorParallelSize (%d)" (.name | default "unknown") $mbp $tpP) }}
{{- end }}
{{- end }}
{{- $tpD := int (.pd.decode.tensorParallelSize | default 1) }}
{{- if and (hasKey .pd.decode "maxNumBatchedTokens") (gt $tpD 0) }}
{{- $mbd := int .pd.decode.maxNumBatchedTokens }}
{{- if ne (mod $mbd $tpD) 0 }}
{{- $errors = append $errors (printf "services[%s].pd.decode.maxNumBatchedTokens (%d) must be a multiple of tensorParallelSize (%d)" (.name | default "unknown") $mbd $tpD) }}
{{- end }}
{{- end }}
{{- end }}
{{- if and .enabled (eq .mode "aggregated") }}
{{- $tpA := int (.aggregated.tensorParallelSize | default 1) }}
{{- if and (hasKey .aggregated "maxNumBatchedTokens") (gt $tpA 0) }}
{{- $mba := int .aggregated.maxNumBatchedTokens }}
{{- if ne (mod $mba $tpA) 0 }}
{{- $errors = append $errors (printf "services[%s].aggregated.maxNumBatchedTokens (%d) must be a multiple of tensorParallelSize (%d)" (.name | default "unknown") $mba $tpA) }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{/* At least one service must be enabled */}}
{{- if not $hasEnabledService }}
{{- $errors = append $errors "at least one service with enabled=true is required" }}
{{- end }}

{{/* If there are any validation errors, fail with all error messages */}}
{{- if $errors }}
{{- $errorMsg := join "\n" $errors }}
{{- printf "\nVALIDATION ERROR:\n%s\n" $errorMsg | fail }}
{{- end }}
{{- end }}

