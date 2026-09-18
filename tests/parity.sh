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
  "repos|host/10-repos|host/repos"
  "backup|host/40-backup-jobs|host/backup"
)

# Известные и намеренные расхождения при сравнении деревьев.
#
# storage/mnt: bash-версия создаёт каталог хранилища НАСТОЯЩИМ mkdir, мимо
# KEEL_FS_ROOT, — на живом хосте она завела бы /mnt/media взаправду. В её
# тестах это прикрывалось заглушкой mkdir в PATH, то есть только внутри
# тестов. Go-версия уводит каталог в песочницу, как и все прочие пути.
# Расхождение — это исправление, а не потеря.
ignore_for() {
  case "$1" in
    storage) printf '%s\n' "mnt" ;;
  esac
}

# Строки, которые из сверки команд исключаются, и почему.
#
# «# записать …»: запись файла командой не выражается, и сравнивается она
# сильнее — сравнением самих файлов после применения.
#
# repos/apt-get update: bash-версия пропускает обновление списка пакетов,
# когда задан KEEL_FS_ROOT, — то есть путает «мы в тесте» с «не обновлять
# списки». Go держит план честным, а от выполнения на живом хосте защищает
# песочница. Расхождение — это исправление.
ignore_cmd_for() {
  printf '%s\n' "^# записать "
  case "$1" in
    repos) printf '%s\n' "^apt-get update" ;;
  esac
}

# Подставной хост. Тот же, что в тестах обеих версий.
fake_host() {
  local root=$1
  mkdir -p "${root}/etc/pve" "${root}/etc/apt/sources.list.d" "${root}/usr/share/keyrings"
  : >"${root}/usr/share/keyrings/proxmox-archive-keyring.gpg"
  cat >"${root}/etc/apt/sources.list.d/pve-enterprise.sources" <<'EOF'
Types: deb
URIs: https://enterprise.proxmox.com/debian/pve
Suites: trixie
Components: pve-enterprise
Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg
EOF
  cat >"${root}/etc/apt/sources.list.d/debian.sources" <<'EOF'
Types: deb
URIs: http://deb.debian.org/debian
Suites: trixie
Components: main contrib
EOF

  # os-release кладём НАСТОЯЩИЙ, копией с этой машины, и это не лень.
  # bash-версия читает /etc/os-release мимо KEEL_FS_ROOT — то есть берёт
  # кодовое имя живой системы, чем бы ни была песочница. Go читает
  # песочницу, как и все прочие пути. Чтобы сверка проверяла сборку
  # репозитория, а не эту разницу, обе версии должны увидеть одно и то же;
  # сама разница описана в docs/PORT.md.
  cp /etc/os-release "${root}/etc/os-release" 2>/dev/null || true
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
    repos)
      printf '{ "host": { "repos": "no-subscription" } }\n'
      ;;
    backup)
      cat <<'EOF'
{
  "backup": {
    "schedule": "02:00", "storage": "local", "mode": "snapshot",
    "guests": [100, 101], "keep_last": 3, "compress": "zstd"
  }
}
EOF
      ;;
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

# Применение в песочницу: обе версии правят файлы по-настоящему, но внутри
# KEEL_FS_ROOT. Сравнение получившихся деревьев — проверка сильнее сверки
# команд: она ловит и разницу в содержимом файлов.
bash_apply() {
  local module=$1
  KEEL_UI=plain KEEL_MODE=yes "${ROOT}/bin/keel" --yes --only "$module" apply >/dev/null 2>&1 || true
}

go_apply() {
  local provider=$1
  "${ROOT}/.keel-parity/keel" apply --yes --only "$provider" >/dev/null 2>&1 || true
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

  filter="${T}/filter.txt"
  ignore_cmd_for "$name" >"$filter"

  bash_commands "$module" | grep -vEf "$filter" | sort >"${T}/bash.txt" || true
  go_commands   "$provider" | grep -vEf "$filter" | sort >"${T}/go.txt" || true

  bad=0

  if ! diff -q "${T}/bash.txt" "${T}/go.txt" >/dev/null; then
    printf '%s✗%s %-12s команды разошлись:\n' "$C_RED" "$C_RESET" "$name"
    diff -u --label "bash" --label "go" "${T}/bash.txt" "${T}/go.txt" | sed 's/^/    /'
    bad=1
  fi

  # Теперь то же самое применением: каждая версия правит свою копию хоста.
  cp -a "${T}/root" "${T}/root-bash"
  cp -a "${T}/root" "${T}/root-go"

  KEEL_FS_ROOT="${T}/root-bash" bash_apply "$module"
  KEEL_FS_ROOT="${T}/root-go"   go_apply   "$provider"

  exclude=()
  while IFS= read -r skip; do
    [[ -n "$skip" ]] && exclude+=(--exclude="$skip")
  done < <(ignore_for "$name")

  if ! diff -r -q "${exclude[@]+"${exclude[@]}"}" "${T}/root-bash" "${T}/root-go" >/dev/null 2>&1; then
    printf '%s✗%s %-12s файлы после применения разошлись:\n' "$C_RED" "$C_RESET" "$name"
    diff -r -u "${exclude[@]+"${exclude[@]}"}" "${T}/root-bash" "${T}/root-go" 2>&1 | head -40 | sed 's/^/    /'
    bad=1
  fi

  if (( bad == 0 )); then
    printf '%s✓%s %-12s команд: %s · файлы после применения совпали\n' \
      "$C_GREEN" "$C_RESET" "$name" "$(wc -l <"${T}/bash.txt" | tr -d ' ')"
    PASSED=$(( PASSED + 1 ))
  else
    FAILED=$(( FAILED + 1 ))
  fi
  rm -rf "$T"
done

printf '\n%sСовпало: %d · разошлось: %d%s\n' "$C_DIM" "$PASSED" "$FAILED" "$C_RESET"
rm -rf "${ROOT}/.keel-parity"
(( FAILED == 0 ))
