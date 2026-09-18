#!/usr/bin/env bash
#
# Сверка порта: bash-версия и Go-версия должны отдавать хосту одни и те же
# команды.
#
# Это самая честная проверка переписывания. Тесты каждой версии проверяют
# её собственные ожидания — а здесь две независимые реализации сверяются
# друг с другом на одном и том же манифесте и одном и том же подставном
# хосте. Разошлись — значит при переносе что-то потерялось.
#
#   ./tests/parity.sh              все области, что уже перенесены
#   ./tests/parity.sh storage      только одну

set -Eeuo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
ROOT=$(pwd)
FILTER=${1:-}

if [[ -t 1 ]]; then
  C_RESET=$'\033[0m'; C_GREEN=$'\033[32m'; C_RED=$'\033[31m'; C_DIM=$'\033[2m'
else
  C_RESET=''; C_GREEN=''; C_RED=''; C_DIM=''
fi

PASSED=0; FAILED=0

# Соответствие имён: в bash модуль назывался по имени файла с номером,
# в Go провайдер зовётся по области.
# случай|модуль bash|провайдер Go
CASES=(
  "storage|host/30-storage|host/storage"
)

# Подставной хост. Тот же, что в тестах обеих версий.
fake_host() {
  local root=$1
  mkdir -p "${root}/etc/pve"
  cat >"${root}/etc/pve/storage.cfg" <<'EOF'
dir: local
	path /var/lib/vz
	content iso,vztmpl,backup

lvmthin: local-lvm
	thinpool data
	vgname pve
	content rootdir,images
EOF
}

manifest_for() {
  case "$1" in
    storage)
      cat <<'EOF'
{
  "storages": [
    { "name": "local", "content": ["iso", "vztmpl", "backup", "snippets"] },
    { "name": "media", "type": "dir", "path": "/mnt/media", "content": ["iso"] }
  ]
}
EOF
      ;;
    *) printf '{}\n' ;;
  esac
}

# Команды bash-версии: в режиме показа run() печатает описание, а следом
# саму команду с отступом. Берём строки с отступом.
bash_commands() {
  local module=$1
  KEEL_UI=plain "${ROOT}/bin/keel" --dry-run --only "$module" apply 2>/dev/null \
    | sed -n 's/^    \(.*\)$/\1/p'
}

# Команды Go-версии: keel сам печатает их по одной на строку.
go_commands() {
  local provider=$1
  "${ROOT}/.keel-parity/keel" plan --commands --only "$provider" 2>/dev/null
}

printf 'Сверка команд: bash против Go\n'
mkdir -p "${ROOT}/.keel-parity"
go build -o "${ROOT}/.keel-parity/keel" ./cmd/keel

for entry in "${CASES[@]}"; do
  IFS='|' read -r name module provider <<<"$entry"
  [[ -z "$FILTER" || "$name" == *"$FILTER"* ]] || continue

  T=$(mktemp -d)
  trap 'rm -rf "$T"' RETURN
  mkdir -p "${T}/home" "${T}/root"
  fake_host "${T}/root"
  manifest_for "$name" >"${T}/home/host.json"

  export KEEL_HOME="${T}/home" KEEL_FS_ROOT="${T}/root" \
         KEEL_MANIFEST="${T}/home/host.json" KEEL_ALLOW_NON_PVE=1

  bash_commands "$module" | sort >"${T}/bash.txt"
  go_commands   "$provider" | sort >"${T}/go.txt"

  if [[ ! -s "${T}/bash.txt" ]]; then
    printf '%s✗%s %-12s bash-версия не выдала ни одной команды\n' "$C_RED" "$C_RESET" "$name"
    FAILED=$(( FAILED + 1 ))
    rm -rf "$T"; continue
  fi

  if diff -q "${T}/bash.txt" "${T}/go.txt" >/dev/null; then
    printf '%s✓%s %-12s совпало команд: %s\n' "$C_GREEN" "$C_RESET" "$name" "$(wc -l <"${T}/bash.txt" | tr -d ' ')"
    PASSED=$(( PASSED + 1 ))
  else
    printf '%s✗%s %-12s команды разошлись:\n' "$C_RED" "$C_RESET" "$name"
    diff -u --label "bash" --label "go" "${T}/bash.txt" "${T}/go.txt" | sed 's/^/    /'
    FAILED=$(( FAILED + 1 ))
  fi
  rm -rf "$T"
done

printf '\n%sСовпало: %d · разошлось: %d%s\n' "$C_DIM" "$PASSED" "$FAILED" "$C_RESET"
rm -rf "${ROOT}/.keel-parity"
(( FAILED == 0 ))
