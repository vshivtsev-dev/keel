#!/usr/bin/env bash
#
# Тесты keel.
#
# Нарочно без bats и прочих зависимостей: проект обещает работать на голом
# Proxmox, и его собственные тесты держатся того же правила. Нужен только bash.
#
#   ./tests/run.sh            все проверки
#   ./tests/run.sh config     только тесты с "config" в названии

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1
KEEL_ROOT=$(pwd)
export KEEL_ROOT
FILTER=${1:-}

PASSED=0; FAILED=0; FAILED_NAMES=()

# --- Мелкие помощники --------------------------------------------------------

fail() { printf 'ОШИБКА: %s\n' "$*" >&2; return 1; }

assert_eq() {
  [[ "$1" == "$2" ]] || fail "ожидалось «$2», получено «$1» ${3:+($3)}"
}

assert_contains() {
  [[ "$1" == *"$2"* ]] || fail "в выводе нет «$2». Вывод: $1"
}

assert_rc() {
  local expected=$1 actual=$2 what=${3:-}
  (( expected == actual )) || fail "ожидался код ${expected}, получен ${actual} ${what:+($what)}"
}

# Загрузить библиотеки в текущую (под)оболочку и увести все пути во временный каталог
load_keel() {
  export KEEL_LOG_DIR="${T}/log"
  export KEEL_STATE_DIR="${T}/state"
  export KEEL_BACKUP_DIR="${T}/state/backups"
  export KEEL_UI="plain"
  # shellcheck source=../lib/core.sh
  source "${KEEL_ROOT}/lib/core.sh"
  # shellcheck source=../lib/ui.sh
  source "${KEEL_ROOT}/lib/ui.sh"
  # shellcheck source=../lib/config.sh
  source "${KEEL_ROOT}/lib/config.sh"
  # shellcheck source=../lib/state.sh
  source "${KEEL_ROOT}/lib/state.sh"
  # shellcheck source=../lib/pve.sh
  source "${KEEL_ROOT}/lib/pve.sh"
  # shellcheck source=../lib/modules.sh
  source "${KEEL_ROOT}/lib/modules.sh"
  core_init
  state_init
}

# it "название" функция — каждый тест в своей подоболочке и своём каталоге
it() {
  local name=$1 fn=$2
  if [[ -n "$FILTER" && "$name" != *"$FILTER"* ]]; then return 0; fi
  local out rc=0
  out=$(
    set -uo pipefail
    T=$(mktemp -d)
    export T
    trap 'rm -rf "$T"' EXIT
    load_keel
    "$fn"
  ) || rc=$?
  if (( rc == 0 )); then
    printf '  ✓ %s\n' "$name"
    PASSED=$(( PASSED + 1 ))
  else
    printf '  ✗ %s\n' "$name"
    [[ -n "$out" ]] && printf '%s\n' "$out" | sed 's/^/      /'
    FAILED=$(( FAILED + 1 ))
    FAILED_NAMES+=("$name")
  fi
}

# --- Манифест ----------------------------------------------------------------

test_config_comments() {
  cat >"${T}/m.json" <<'EOF'
{
  # комментарий, ради которого всё затевалось
  "host": { "repos": "no-subscription" },  # и в конце строки тоже
  "guests": []
}
EOF
  config_load "${T}/m.json"
  assert_eq "$(config_get host.repos)" "no-subscription" "значение из-под комментариев"
  assert_eq "$(config_len guests)" "0" "пустой массив гостей"
}

test_config_trailing_comma() {
  printf '{ "host": { "repos": "test", }, }' >"${T}/m.json"
  config_load "${T}/m.json"
  assert_eq "$(config_get host.repos)" "test" "висячая запятая не мешает"
}

test_config_url_not_eaten_as_comment() {
  # Решётка внутри строки — часть значения, а не начало комментария
  printf '{ "host": { "repos": "no-subscription" }, "storages": [ { "name": "a#b" } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  assert_eq "$(config_get storages.0.name)" "a#b" "решётка внутри строки"
}

test_config_nested_and_arrays() {
  cat >"${T}/m.json" <<'EOF'
{
  "guests": [
    { "id": 100, "name": "haos",    "profile": "haos",    "start_on_boot": true },
    { "id": 101, "name": "desktop", "profile": "desktop", "packages": ["vlc", "firefox"] }
  ]
}
EOF
  config_load "${T}/m.json"
  assert_eq "$(config_len guests)" "2"
  assert_eq "$(config_get guests.1.name)" "desktop"
  assert_eq "$(config_len guests.1.packages)" "2"
  assert_eq "$(config_get guests.1.packages.0)" "vlc"
  config_bool guests.0.start_on_boot || fail "булево true не распозналось"
}

test_config_validate_catches_typos() {
  printf '{ "hosts": {} }' >"${T}/m.json"
  config_load "${T}/m.json"
  local rc=0; config_validate >/dev/null 2>&1 || rc=$?
  assert_rc 1 "$rc" "неизвестный раздел должен быть ошибкой"
}

test_config_validate_requires_guest_fields() {
  printf '{ "guests": [ { "name": "x" } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  local rc=0; config_validate >/dev/null 2>&1 || rc=$?
  assert_rc 1 "$rc" "гость без id и profile — ошибка"
}

test_config_missing_file_is_not_an_error() {
  local rc=0; config_load "${T}/нет-такого.json" || rc=$?
  assert_rc 0 "$rc" "отсутствие манифеста не должно ронять keel"
  assert_eq "${#KEEL_CFG[@]}" "0" "конфиг должен остаться пустым"
}

# --- Ядро --------------------------------------------------------------------

test_core_cmd_str_quotes() {
  assert_eq "$(cmd_str echo 'два слова')" "echo 'два слова'"
  assert_eq "$(cmd_str apt-get update)" "apt-get update"
}

test_core_pad_counts_characters_not_bytes() {
  # Русские подписи многобайтовые: если считать байты, колонки разъезжаются
  local padded; padded=$(pad "Версия" 10)
  assert_eq "${#padded}" "10" "дополнение должно считаться в символах"
  assert_eq "$(pad "слишком-длинная-подпись" 5)" "слишком-длинная-подпись" \
    "строку длиннее ширины не режем"
}

test_core_dry_run_executes_nothing() {
  export KEEL_MODE="dry"
  run "создать файл" touch "${T}/должен-отсутствовать" >/dev/null
  [[ ! -e "${T}/должен-отсутствовать" ]] || fail "в режиме dry команда выполнилась"
}

test_core_run_write_adds_newline_and_is_idempotent() {
  export KEEL_MODE="yes"
  printf 'строка' | run_write "записать" "${T}/f.conf" >/dev/null
  assert_eq "$(tail -c1 "${T}/f.conf" | od -An -c | tr -d ' ')" '\n' "файл должен кончаться переводом строки"

  local before after
  before=$(stat -c %Y "${T}/f.conf")
  sleep 1
  printf 'строка' | run_write "записать повторно" "${T}/f.conf" >/dev/null
  after=$(stat -c %Y "${T}/f.conf")
  assert_eq "$before" "$after" "повторная запись того же содержимого не должна трогать файл"
}

test_core_run_write_makes_backup() {
  export KEEL_MODE="yes"
  printf 'было' | run_write "первая запись" "${T}/f.conf" >/dev/null
  printf 'стало' | run_write "вторая запись" "${T}/f.conf" >/dev/null
  local found
  found=$(find "$KEEL_BACKUP_DIR" -name 'f.conf' 2>/dev/null | head -n1)
  [[ -n "$found" ]] || fail "резервная копия не создана"
  assert_eq "$(cat "$found")" "было" "в копии должно лежать прежнее содержимое"
}

# --- Модуль репозиториев -----------------------------------------------------

# Заготовка «свежеустановленного хоста» в новом формате (PVE 9)
_fake_host_deb822() {
  KEEL_FS_ROOT="${T}/root"
  mkdir -p "${KEEL_FS_ROOT}/etc/apt/sources.list.d"
  cat >"${KEEL_FS_ROOT}/etc/apt/sources.list.d/pve-enterprise.sources" <<'EOF'
Types: deb
URIs: https://enterprise.proxmox.com/debian/pve
Suites: trixie
Components: pve-enterprise
Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg
EOF
}

_fake_host_list() {
  KEEL_FS_ROOT="${T}/root"
  mkdir -p "${KEEL_FS_ROOT}/etc/apt/sources.list.d"
  cat >"${KEEL_FS_ROOT}/etc/apt/sources.list.d/pve-enterprise.list" <<'EOF'
deb https://enterprise.proxmox.com/debian/pve bookworm pve-enterprise
EOF
}

_manifest_repos() {
  printf '{ "host": { "repos": "%s" } }' "$1" >"${T}/m.json"
  config_load "${T}/m.json"
}

test_repos_zero_rule_without_manifest() {
  _fake_host_deb822
  config_load "${T}/нет.json"
  local rc=0
  # shellcheck source=../modules/host/10-repos.sh
  ( source "${KEEL_ROOT}/modules/host/10-repos.sh"; mod_check ) >/dev/null 2>&1 || rc=$?
  assert_rc 20 "$rc" "без ключа в манифесте модуль обязан ничего не делать"
}

test_repos_apply_then_idempotent() {
  _fake_host_deb822
  _manifest_repos "no-subscription"
  export KEEL_MODE="yes"

  local rc=0
  ( source "${KEEL_ROOT}/modules/host/10-repos.sh"; mod_check ) >/dev/null 2>&1 || rc=$?
  assert_rc 10 "$rc" "на свежем хосте должны быть изменения"

  ( source "${KEEL_ROOT}/modules/host/10-repos.sh"; mod_apply ) >/dev/null 2>&1 \
    || fail "применение не удалось"

  local f="${KEEL_FS_ROOT}/etc/apt/sources.list.d/pve-no-subscription.sources"
  [[ -f "$f" ]] || fail "файл бесплатного репозитория не создан"
  assert_contains "$(cat "$f")" "pve-no-subscription"
  assert_contains "$(cat "${KEEL_FS_ROOT}/etc/apt/sources.list.d/pve-enterprise.sources")" "Enabled: false"

  rc=0
  ( source "${KEEL_ROOT}/modules/host/10-repos.sh"; mod_check ) >/dev/null 2>&1 || rc=$?
  assert_rc 0 "$rc" "повторная проверка не должна находить работу"

  rc=0
  ( source "${KEEL_ROOT}/modules/host/10-repos.sh"; mod_verify ) >/dev/null 2>&1 || rc=$?
  assert_rc 0 "$rc" "проверка должна подтвердить результат"
}

test_repos_old_list_format() {
  _fake_host_list
  _manifest_repos "no-subscription"
  export KEEL_MODE="yes"
  ( source "${KEEL_ROOT}/modules/host/10-repos.sh"; mod_apply ) >/dev/null 2>&1 \
    || fail "применение не удалось"
  local f="${KEEL_FS_ROOT}/etc/apt/sources.list.d/pve-no-subscription.list"
  [[ -f "$f" ]] || fail "файл в старом формате не создан"
  assert_contains "$(cat "$f")" "deb http://download.proxmox.com/debian/pve"
  assert_contains "$(cat "${KEEL_FS_ROOT}/etc/apt/sources.list.d/pve-enterprise.list")" "# Выключено keel"
}

test_repos_rejects_unknown_value() {
  _fake_host_deb822
  _manifest_repos "халява"
  local rc=0
  ( source "${KEEL_ROOT}/modules/host/10-repos.sh"; mod_check ) >/dev/null 2>&1 || rc=$?
  assert_rc 1 "$rc" "неизвестное значение host.repos должно быть ошибкой"
}

test_repos_never_touches_real_root_in_tests() {
  # Страховка от собственной ошибки: тесты не должны писать мимо KEEL_FS_ROOT
  _fake_host_deb822
  _manifest_repos "no-subscription"
  export KEEL_MODE="yes"
  ( source "${KEEL_ROOT}/modules/host/10-repos.sh"; mod_apply ) >/dev/null 2>&1 || true
  [[ ! -f /etc/apt/sources.list.d/pve-no-subscription.sources ]] \
    || fail "тест записал файл в настоящую систему"
}

# --- Запуск ------------------------------------------------------------------

printf '\nМанифест\n'
it "config: комментарии в JSON"                 test_config_comments
it "config: висячие запятые"                    test_config_trailing_comma
it "config: решётка внутри строки не комментарий" test_config_url_not_eaten_as_comment
it "config: вложенность, массивы, булевы"       test_config_nested_and_arrays
it "config: ловит неизвестный раздел"           test_config_validate_catches_typos
it "config: требует id и profile у гостя"       test_config_validate_requires_guest_fields
it "config: нет файла — не ошибка"              test_config_missing_file_is_not_an_error

printf '\nЯдро\n'
it "core: экранирование команд"                 test_core_cmd_str_quotes
it "core: выравнивание по символам, не байтам"  test_core_pad_counts_characters_not_bytes
it "core: dry-run ничего не выполняет"          test_core_dry_run_executes_nothing
it "core: run_write идемпотентен"               test_core_run_write_adds_newline_and_is_idempotent
it "core: run_write делает резервную копию"     test_core_run_write_makes_backup

printf '\nМодуль репозиториев\n'
it "repos: правило нуля без манифеста"          test_repos_zero_rule_without_manifest
it "repos: применение и идемпотентность"        test_repos_apply_then_idempotent
it "repos: старый формат .list"                 test_repos_old_list_format
it "repos: отвергает неизвестное значение"      test_repos_rejects_unknown_value
it "repos: не трогает настоящую систему"        test_repos_never_touches_real_root_in_tests

printf '\nСтатические проверки\n'
if ./tests/lint-run-guard.sh >/dev/null 2>&1; then
  printf '  ✓ run-guard: изменения только через run()\n'; PASSED=$(( PASSED + 1 ))
else
  printf '  ✗ run-guard\n'; ./tests/lint-run-guard.sh 2>&1 | sed 's/^/      /'
  FAILED=$(( FAILED + 1 )); FAILED_NAMES+=("run-guard")
fi

if command -v shellcheck >/dev/null 2>&1; then
  if LC_ALL=C.UTF-8 shellcheck -x -S warning bin/keel lib/*.sh modules/*/*.sh tests/*.sh >/dev/null 2>&1; then
    printf '  ✓ shellcheck\n'; PASSED=$(( PASSED + 1 ))
  else
    printf '  ✗ shellcheck\n'
    LC_ALL=C.UTF-8 shellcheck -x -S warning bin/keel lib/*.sh modules/*/*.sh tests/*.sh 2>&1 | sed 's/^/      /'
    FAILED=$(( FAILED + 1 )); FAILED_NAMES+=("shellcheck")
  fi
else
  printf '  · shellcheck не установлен, пропускаю\n'
fi

printf '\nПройдено: %d, провалено: %d\n' "$PASSED" "$FAILED"
if (( FAILED )); then
  printf 'Провалились: %s\n' "${FAILED_NAMES[*]}"
  exit 1
fi
