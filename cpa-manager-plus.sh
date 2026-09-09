#!/usr/bin/env bash
set -euo pipefail
umask 077

app_name="cpa-manager-plus"
invocation_dir="$(pwd -P)"
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
local_dir="${root_dir}/.local"
build_dir="${local_dir}/build"
data_dir="${local_dir}/data"
temp_dir="${local_dir}/tmp"
control_dir="${local_dir}/control"
control_source="${root_dir}/bin/native/cpa-manager-plusctl.sh"
control_script="${control_dir}/cpa-manager-plusctl"
server_source="${root_dir}/apps/manager-server"
web_html="${root_dir}/apps/web/dist/index.html"
binary_path="${build_dir}/${app_name}"
next_binary_path="${build_dir}/${app_name}.next"
previous_binary_path="${build_dir}/${app_name}.previous"
admin_key_file="${data_dir}/admin.key"
admin_key_state_file="${data_dir}/.admin-key-initialized"
port_file="${control_dir}/run/source.port"
source_host="${CPA_MANAGER_PLUS_SOURCE_HOST:-127.0.0.1}"
external_admin_key_configured='false'
if [ -n "${CPA_MANAGER_ADMIN_KEY:-}" ] || [ -n "${CPA_MANAGER_ADMIN_KEY_FILE:-}" ]; then
  external_admin_key_configured='true'
fi
default_port="18317"
requested_port=""
action="start"
action_args=()
build_work_dir=""
admin_key_temp_file=""

usage() {
  cat <<'EOF'
Usage: ./cpa-manager-plus.sh [command] [--port <port>]

Commands:
  build      Install dependencies and compile the web panel and Go service.
  start      Start the source-built service; build first when needed.
  stop       Stop the managed source-built service.
  restart    Restart without rebuilding.
  rebuild    Build a candidate while serving, then replace and restart.
  status     Show process and HTTP health status.
  logs       Show logs; accepts a line count or -f/--follow.
  admin-key  Show the source runtime's locally managed admin key.
  help       Show this help.

Examples:
  ./cpa-manager-plus.sh start
  ./cpa-manager-plus.sh start --port 18318
  ./cpa-manager-plus.sh rebuild --port=18317
  ./cpa-manager-plus.sh logs 120
  ./cpa-manager-plus.sh admin-key
  ./cpa-manager-plus.sh stop

Environment overrides:
  CPA_MANAGER_PLUS_SOURCE_HOST   Bind host, default: 127.0.0.1
  CPA_MANAGER_PLUS_SOURCE_PORT   Default port, default: 18317
  CPA_MANAGER_PLUS_SKIP_NPM_CI   Set to 1 to reuse installed dependencies
  USAGE_DATA_DIR and related Manager Server variables remain supported.

Source runtime files are stored under .local/.
EOF
}

cleanup() {
  if [ -n "${build_work_dir}" ] && [ -d "${build_work_dir}" ]; then
    rm -rf -- "${build_work_dir}"
  fi
  if [ -n "${admin_key_temp_file}" ]; then
    rm -f -- "${admin_key_temp_file}"
  fi
}
trap cleanup EXIT INT TERM

normalize_port() {
  local value="$1"
  local source="$2"
  if [[ ! "${value}" =~ ^[0-9]{1,5}$ ]]; then
    printf 'ERROR: %s must be an integer between 1 and 65535.\n' "${source}" >&2
    return 1
  fi
  local port=$((10#${value}))
  if (( port < 1 || port > 65535 )); then
    printf 'ERROR: %s must be between 1 and 65535.\n' "${source}" >&2
    return 1
  fi
  printf '%s' "${port}"
}

parse_args() {
  local positionals=()
  while (( $# > 0 )); do
    case "$1" in
      --port)
        if [ -n "${requested_port}" ]; then
          echo 'ERROR: --port may only be specified once.' >&2
          exit 1
        fi
        if (( $# < 2 )); then
          echo 'ERROR: --port requires a value.' >&2
          exit 1
        fi
        requested_port="$(normalize_port "$2" '--port')"
        shift 2
        ;;
      --port=*)
        if [ -n "${requested_port}" ]; then
          echo 'ERROR: --port may only be specified once.' >&2
          exit 1
        fi
        requested_port="$(normalize_port "${1#*=}" '--port')"
        shift
        ;;
      *)
        positionals+=("$1")
        shift
        ;;
    esac
  done

  if (( ${#positionals[@]} > 0 )); then
    action="${positionals[0]}"
  fi
  if (( ${#positionals[@]} > 1 )); then
    action_args=("${positionals[@]:1}")
  fi

  case "${action}" in
    build|start|stop|restart|rebuild|status|logs|admin-key|help|-h|--help) ;;
    *)
      printf 'ERROR: Unknown command: %s\n' "${action}" >&2
      exit 1
      ;;
  esac
  if [ "${action}" != 'logs' ] && (( ${#action_args[@]} > 0 )); then
    printf 'ERROR: Unexpected argument for %s: %s\n' "${action}" "${action_args[0]}" >&2
    exit 1
  fi
  if [ "${action}" = 'logs' ] && (( ${#action_args[@]} > 1 )); then
    echo 'ERROR: logs accepts at most one line count or -f/--follow.' >&2
    exit 1
  fi
}

ensure_layout() {
  mkdir -p "${build_dir}" "${data_dir}" "${temp_dir}" "${control_dir}"
  chmod 700 "${local_dir}" "${build_dir}" "${data_dir}" "${temp_dir}" "${control_dir}"
  if [ ! -f "${control_source}" ]; then
    printf 'ERROR: Native process controller does not exist: %s\n' "${control_source}" >&2
    exit 1
  fi
  cp "${control_source}" "${control_script}"
  chmod 700 "${control_script}"
  export CPA_MANAGER_PLUS_BIN="${binary_path}"
}

configured_port() {
  if [ -n "${requested_port}" ]; then
    printf '%s' "${requested_port}"
  elif [ -n "${CPA_MANAGER_PLUS_SOURCE_PORT:-}" ]; then
    normalize_port "${CPA_MANAGER_PLUS_SOURCE_PORT}" 'CPA_MANAGER_PLUS_SOURCE_PORT'
  else
    printf '%s' "${default_port}"
  fi
}

stored_port() {
  if [ -f "${port_file}" ]; then
    local stored
    stored="$(tr -d '[:space:]' <"${port_file}")"
    if normalize_port "${stored}" 'stored source port' 2>/dev/null; then
      return
    fi
    rm -f -- "${port_file}"
  fi
  configured_port
}

set_runtime_environment() {
  local port="$1"
  export CPA_MANAGER_PLUS_BIN="${binary_path}"
  export HTTP_ADDR="${source_host}:${port}"
  export USAGE_DATA_DIR="${USAGE_DATA_DIR:-${data_dir}}"
  if [[ "${USAGE_DATA_DIR}" != /* ]]; then
    export USAGE_DATA_DIR="${invocation_dir}/${USAGE_DATA_DIR}"
  fi
  export USAGE_DB_PATH="${USAGE_DB_PATH:-${USAGE_DATA_DIR}/usage.sqlite}"
  if [[ "${USAGE_DB_PATH}" != /* ]]; then
    export USAGE_DB_PATH="${invocation_dir}/${USAGE_DB_PATH}"
  fi
  export CPA_MANAGER_DATA_KEY_PATH="${CPA_MANAGER_DATA_KEY_PATH:-${USAGE_DATA_DIR}/data.key}"
  if [[ "${CPA_MANAGER_DATA_KEY_PATH}" != /* ]]; then
    export CPA_MANAGER_DATA_KEY_PATH="${invocation_dir}/${CPA_MANAGER_DATA_KEY_PATH}"
  fi
  export USAGE_CORS_ORIGINS="${USAGE_CORS_ORIGINS:-*}"
  if [ "${external_admin_key_configured}" = 'false' ]; then
    export CPA_MANAGER_ADMIN_KEY_FILE="${admin_key_file}"
  elif [ -n "${CPA_MANAGER_ADMIN_KEY_FILE:-}" ] && [[ "${CPA_MANAGER_ADMIN_KEY_FILE}" != /* ]]; then
    export CPA_MANAGER_ADMIN_KEY_FILE="${invocation_dir}/${CPA_MANAGER_ADMIN_KEY_FILE}"
  fi
}

managed_admin_key() {
  if [ ! -f "${admin_key_file}" ]; then
    printf 'ERROR: Managed admin key does not exist yet: %s\n' "${admin_key_file}" >&2
    return 1
  fi
  local admin_key
  admin_key="$(tr -d '[:space:]' <"${admin_key_file}")"
  if [[ ! "${admin_key}" =~ ^cpamp_[0-9A-Za-z]{32}$ ]]; then
    printf 'ERROR: Managed admin key file is empty or invalid: %s\n' "${admin_key_file}" >&2
    return 1
  fi
  printf '%s' "${admin_key}"
}

write_private_text_file() {
  local path="$1"
  local value="$2"
  (umask 077 && printf '%s\n' "${value}" >"${path}")
  chmod 600 "${path}"
}

trim_whitespace() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "${value}"
}

effective_admin_key() {
  local admin_key
  if [ -n "${CPA_MANAGER_ADMIN_KEY:-}" ]; then
    admin_key="$(trim_whitespace "${CPA_MANAGER_ADMIN_KEY}")"
  elif [ -n "${CPA_MANAGER_ADMIN_KEY_FILE:-}" ]; then
    if [ ! -f "${CPA_MANAGER_ADMIN_KEY_FILE}" ]; then
      printf 'ERROR: Admin key file does not exist: %s\n' "${CPA_MANAGER_ADMIN_KEY_FILE}" >&2
      return 1
    fi
    admin_key="$(trim_whitespace "$(<"${CPA_MANAGER_ADMIN_KEY_FILE}")")"
  else
    admin_key="$(managed_admin_key)"
  fi
  if [ -z "${admin_key}" ]; then
    echo 'ERROR: Admin key is empty.' >&2
    return 1
  fi
  printf '%s' "${admin_key}"
}

sha256_value() {
  local value="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s' "${value}" | sha256sum | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    printf '%s' "${value}" | shasum -a 256 | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    printf '%s' "${value}" | openssl dgst -sha256 | awk '{print $NF}'
  else
    echo 'ERROR: sha256sum, shasum, or openssl is required for admin key state tracking.' >&2
    return 1
  fi
}

desired_admin_key_state() {
  if [ ! -s "${USAGE_DB_PATH}" ]; then
    return 0
  fi
  local database_dir database_path database_identity admin_key_hash
  database_dir="$(cd "$(dirname "${USAGE_DB_PATH}")" && pwd -P)"
  database_path="${database_dir}/$(basename "${USAGE_DB_PATH}")"
  if database_identity="$(stat -c '%d:%i:%W' "${USAGE_DB_PATH}" 2>/dev/null)"; then
    :
  else
    database_identity="$(stat -f '%d:%i:%B' "${USAGE_DB_PATH}")"
  fi
  admin_key_hash="$(sha256_value "$(effective_admin_key)")"
  sha256_value "unix-v1|${database_path}|${database_identity}|${admin_key_hash}"
}

stored_admin_key_state() {
  if [ ! -f "${admin_key_state_file}" ]; then
    return 0
  fi
  tr -d '[:space:]' <"${admin_key_state_file}"
}

ensure_admin_key_configuration() {
  if [ "${external_admin_key_configured}" = 'false' ] && [ ! -f "${admin_key_file}" ]; then
    local random_hex
    random_hex="$(od -An -N16 -tx1 /dev/urandom | tr -d '[:space:]')"
    write_private_text_file "${admin_key_file}" "cpamp_${random_hex}"
    rm -f -- "${admin_key_state_file}"
  fi

  effective_admin_key >/dev/null
  local desired_state stored_state reset_key_file
  desired_state="$(desired_admin_key_state)"
  if [ -z "${desired_state}" ]; then
    return 0
  fi
  stored_state="$(stored_admin_key_state)"
  if [ "${desired_state}" = "${stored_state}" ]; then
    return 0
  fi

  if [ -n "${CPA_MANAGER_ADMIN_KEY:-}" ]; then
    admin_key_temp_file="${temp_dir}/admin-key-reset.$$.${RANDOM}"
    write_private_text_file "${admin_key_temp_file}" "$(effective_admin_key)"
    reset_key_file="${admin_key_temp_file}"
  else
    reset_key_file="${CPA_MANAGER_ADMIN_KEY_FILE}"
  fi

  echo '==> Synchronizing the SQLite admin credential with the configured key'
  "${binary_path}" reset-admin-key \
    --db-path "${USAGE_DB_PATH}" \
    --admin-key-file "${reset_key_file}"
  if [ -n "${admin_key_temp_file}" ]; then
    rm -f -- "${admin_key_temp_file}"
    admin_key_temp_file=""
  fi
  write_private_text_file "${admin_key_state_file}" "${desired_state}"
}

mark_admin_key_state_initialized() {
  local desired_state
  desired_state="$(desired_admin_key_state)"
  if [ -n "${desired_state}" ] && [ "${desired_state}" != "$(stored_admin_key_state)" ]; then
    write_private_text_file "${admin_key_state_file}" "${desired_state}"
  fi
}

show_admin_key() {
  if [ "${external_admin_key_configured}" = 'true' ]; then
    echo 'ERROR: The admin key is externally managed through CPA_MANAGER_ADMIN_KEY or CPA_MANAGER_ADMIN_KEY_FILE.' >&2
    return 1
  fi
  managed_admin_key
  printf '\n'
}

health_url() {
  local port="$1"
  local health_host="${source_host}"
  case "${health_host}" in
    0.0.0.0|::|'[::]') health_host='127.0.0.1' ;;
  esac
  if [[ "${health_host}" == *:* ]] && [[ "${health_host}" != \[*\] ]]; then
    health_host="[${health_host}]"
  fi
  printf 'http://%s:%s/health' "${health_host}" "${port}"
}

source_running() {
  "${control_script}" status >/dev/null 2>&1
}

build_app() {
  local output_path="$1"
  command -v npm >/dev/null 2>&1 || {
    echo 'ERROR: npm was not found. Install Node.js 22+ first.' >&2
    return 1
  }
  command -v go >/dev/null 2>&1 || {
    echo 'ERROR: go was not found. Install Go 1.24+ first.' >&2
    return 1
  }

  echo '==> Building web panel'
  (
    cd "${root_dir}"
    if [ "${CPA_MANAGER_PLUS_SKIP_NPM_CI:-0}" != '1' ]; then
      npm ci
    fi
    npm run build
  )
  if [ ! -f "${web_html}" ]; then
    printf 'ERROR: Web build did not create %s\n' "${web_html}" >&2
    return 1
  fi

  build_work_dir="$(mktemp -d "${temp_dir}/source-build.XXXXXX")"
  echo '==> Staging Manager Server source'
  local staged_server="${build_work_dir}/manager-server"
  mkdir -p "${staged_server}"
  local source_entry
  shopt -s dotglob nullglob
  for source_entry in "${server_source}"/*; do
    if [ "$(basename "${source_entry}")" = 'node_modules' ]; then
      continue
    fi
    cp -R "${source_entry}" "${staged_server}/"
  done
  shopt -u dotglob nullglob
  cp "${web_html}" "${staged_server}/internal/httpapi/web/management.html"

  mkdir -p "$(dirname "${output_path}")"
  rm -f -- "${output_path}"
  echo '==> Building Go service'
  (
    cd "${staged_server}"
    CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "${output_path}" ./cmd/cpa-manager-plus
  )
  chmod 700 "${output_path}"
  rm -rf -- "${build_work_dir}"
  build_work_dir=""
  printf 'Build complete: %s\n' "${output_path}"
}

wait_for_health() {
  local port="$1"
  local timeout="${STARTUP_TIMEOUT_SECONDS:-60}"
  local url deadline
  url="$(health_url "${port}")"
  deadline=$((SECONDS + timeout))
  while (( SECONDS < deadline )); do
    if command -v curl >/dev/null 2>&1; then
      if curl --silent --fail --max-time 2 "${url}" >/dev/null 2>&1; then
        printf 'Healthy: %s\n' "${url}"
        return 0
      fi
    elif command -v wget >/dev/null 2>&1; then
      if wget -q -T 2 -O /dev/null "${url}" >/dev/null 2>&1; then
        printf 'Healthy: %s\n' "${url}"
        return 0
      fi
    else
      echo 'ERROR: curl or wget is required for the startup health check.' >&2
      return 1
    fi
    sleep 0.5
  done
  printf 'ERROR: Service did not become healthy within %s seconds: %s\n' "${timeout}" "${url}" >&2
  return 1
}

start_app() {
  local port="$1"
  if [ ! -x "${binary_path}" ]; then
    build_app "${binary_path}"
  fi
  set_runtime_environment "${port}"

  if source_running; then
    local active_port
    active_port="$(stored_port)"
    if [ -n "${requested_port}" ] && [ "${requested_port}" != "${active_port}" ]; then
      printf "ERROR: Service is already running on port %s. Use restart --port %s to change it.\n" \
        "${active_port}" "${requested_port}" >&2
      return 1
    fi
    printf 'Source service is already running: http://127.0.0.1:%s\n' "${active_port}"
    return 0
  fi

  ensure_admin_key_configuration

  if "${control_script}" start; then
    mkdir -p "$(dirname "${port_file}")"
    printf '%s' "${port}" >"${port_file}"
    if wait_for_health "${port}"; then
      mark_admin_key_state_initialized
      return 0
    fi
  fi

  "${control_script}" stop >/dev/null 2>&1 || true
  rm -f -- "${port_file}"
  return 1
}

stop_app() {
  "${control_script}" stop
  rm -f -- "${port_file}"
}

show_status() {
  local port="$1"
  set_runtime_environment "${port}"
  "${control_script}" status
  local url
  url="$(health_url "${port}")"
  if command -v curl >/dev/null 2>&1; then
    curl --silent --fail --max-time 3 "${url}" >/dev/null
  elif command -v wget >/dev/null 2>&1; then
    wget -q -T 3 -O /dev/null "${url}" >/dev/null
  else
    echo 'ERROR: curl or wget is required for the health check.' >&2
    return 1
  fi
  printf 'HTTP health: healthy (%s)\n' "${url}"
}

activate_candidate() {
  local port="$1"
  local was_running='false'
  local had_previous_build='false'
  if source_running; then
    was_running='true'
    stop_app
  fi
  if [ -f "${binary_path}" ]; then
    had_previous_build='true'
  fi

  rm -f -- "${previous_binary_path}"
  if [ "${had_previous_build}" = 'true' ]; then
    mv -f -- "${binary_path}" "${previous_binary_path}"
  fi
  mv -f -- "${next_binary_path}" "${binary_path}"
  chmod 700 "${binary_path}"

  if start_app "${port}"; then
    rm -f -- "${previous_binary_path}"
    return 0
  fi

  "${control_script}" stop >/dev/null 2>&1 || true
  rm -f -- "${binary_path}"
  if [ "${had_previous_build}" = 'true' ] && [ -f "${previous_binary_path}" ]; then
    mv -f -- "${previous_binary_path}" "${binary_path}"
    if start_app "${port}"; then
      echo 'WARNING: The new build failed to start; the previous build was restored.' >&2
    else
      echo 'WARNING: The previous build was restored but could not be started.' >&2
    fi
  elif [ "${was_running}" = 'true' ]; then
    echo 'WARNING: No previous build was available for rollback.' >&2
  fi
  echo 'ERROR: The new build failed to start.' >&2
  return 1
}

parse_args "$@"
if [[ "${action}" =~ ^(-h|--help|help)$ ]]; then
  usage
  exit 0
fi

ensure_layout
if [ -n "${requested_port}" ]; then
  port="${requested_port}"
elif source_running; then
  port="$(stored_port)"
else
  port="$(configured_port)"
fi
set_runtime_environment "${port}"

case "${action}" in
  build)
    if source_running; then
      echo "ERROR: The source service is running. Use 'rebuild' to build a candidate before stopping it." >&2
      exit 1
    fi
    build_app "${binary_path}"
    ;;
  start)
    start_app "${port}"
    ;;
  stop)
    stop_app
    ;;
  restart)
    stop_app
    start_app "${port}"
    ;;
  rebuild)
    rm -f -- "${next_binary_path}"
    build_app "${next_binary_path}"
    activate_candidate "${port}"
    ;;
  status)
    show_status "${port}"
    ;;
  logs)
    "${control_script}" logs "${action_args[@]}"
    ;;
  admin-key)
    show_admin_key
    ;;
esac
