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

# Подставные команды Proxmox. Записывают свои аргументы в файл, чтобы тест
# мог проверить, что именно keel собирался выполнить. Заодно гарантия, что
# ни одна настоящая qm/pct/pvesm в тестах не запустится.
stub_commands() {
  export KEEL_STUB_LOG="${T}/commands.log"
  export KEEL_STUB_OUT="${T}/stub-out"
  mkdir -p "${T}/bin" "$KEEL_STUB_OUT"
  : >"$KEEL_STUB_LOG"
  local cmd
  for cmd in "$@"; do
    cat >"${T}/bin/${cmd}" <<STUB
#!/usr/bin/env bash
printf '%s\n' "${cmd} \$*" >> "\$KEEL_STUB_LOG"
arg1=\$(printf '%s' "\${1:-}" | tr -c 'a-zA-Z0-9._-' '_')
if [[ -n "\$arg1" && -f "\$KEEL_STUB_OUT/${cmd}.\$arg1" ]]; then
  cat "\$KEEL_STUB_OUT/${cmd}.\$arg1"
elif [[ -f "\$KEEL_STUB_OUT/${cmd}" ]]; then
  cat "\$KEEL_STUB_OUT/${cmd}"
fi
exit 0
STUB
    chmod +x "${T}/bin/${cmd}"
  done
  export PATH="${T}/bin:${PATH}"
}

# Что подставная команда ответит: stub_says pvesh.get <<<"[]"
stub_says() { cat >"${KEEL_STUB_OUT}/$1"; }

# Все записанные вызовы одной строкой — удобно искать подстроки
stub_log() { cat "$KEEL_STUB_LOG" 2>/dev/null; }

assert_ran() {
  local needle=$1
  stub_log | grep -qF -- "$needle" || fail "не нашёл вызов «${needle}». Было:
$(stub_log)"
}

assert_not_ran() {
  local needle=$1
  stub_log | grep -qF -- "$needle" && fail "команда «${needle}» не должна была выполняться"
  return 0
}

# Прогнать одну функцию модуля в подоболочке, вернуть её код
mod_rc() {
  local module=$1 verb=$2 rc=0
  # shellcheck disable=SC1090  # путь к модулю собирается на лету, это и есть смысл
  ( source "${KEEL_ROOT}/modules/${module}.sh"; "mod_${verb}" ) >/dev/null 2>&1 || rc=$?
  printf '%s' "$rc"
}

mod_out() {
  local module=$1 verb=$2
  # shellcheck disable=SC1090
  ( source "${KEEL_ROOT}/modules/${module}.sh"; "mod_${verb}" ) 2>&1 || true
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


# --- Модуль обновлений -------------------------------------------------------

test_updates_zero_rule() {
  config_load "${T}/нет.json"
  assert_eq "$(mod_rc host/20-updates check)" "20" "без ключа host.updates — ничего не делать"
}

test_updates_nothing_to_do() {
  stub_commands apt-get
  stub_says "apt-get.-s" <<'EOF'
Reading package lists...
0 upgraded, 0 newly installed, 0 to remove.
EOF
  printf '{ "host": { "updates": true } }' >"${T}/m.json"
  config_load "${T}/m.json"
  assert_eq "$(mod_rc host/20-updates check)" "0" "обновлять нечего"
}

test_updates_applies_dist_upgrade() {
  stub_commands apt-get
  stub_says "apt-get.-s" <<'EOF'
Inst pve-manager [8.2.4] (8.2.5 Proxmox:8.2 [amd64])
Inst libpve-common-perl [8.2.1] (8.2.2 Proxmox:8.2 [all])
EOF
  printf '{ "host": { "updates": true } }' >"${T}/m.json"
  config_load "${T}/m.json"
  assert_eq "$(mod_rc host/20-updates check)" "10" "два пакета ждут обновления"
  assert_contains "$(mod_out host/20-updates check)" "pve-manager"

  export KEEL_MODE="yes"
  mod_rc host/20-updates apply >/dev/null
  assert_ran "apt-get update"
  assert_ran "apt-get -y dist-upgrade"
}

# --- Модуль хранилищ ---------------------------------------------------------

_fake_storage_cfg() {
  export KEEL_FS_ROOT="${T}/root"
  mkdir -p "${KEEL_FS_ROOT}/etc/pve"
  cat >"${KEEL_FS_ROOT}/etc/pve/storage.cfg" <<'EOF'
dir: local
	path /var/lib/vz
	content iso,vztmpl,backup

lvmthin: local-lvm
	thinpool data
	vgname pve
	content rootdir,images
EOF
}

test_storage_reads_config() {
  _fake_storage_cfg
  storage_exists local || fail "local должно находиться"
  storage_exists нет-такого && fail "несуществующее хранилище не должно находиться"
  assert_eq "$(storage_type local)" "dir"
  assert_eq "$(storage_path local)" "/var/lib/vz"
  assert_eq "$(storage_content local-lvm)" "rootdir,images"
}

test_storage_adds_missing_content() {
  _fake_storage_cfg
  stub_commands pvesm
  printf '{ "storages": [ { "name": "local", "content": ["iso","vztmpl","backup","snippets"] } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  assert_eq "$(mod_rc host/30-storage check)" "10" "не хватает snippets"
  assert_contains "$(mod_out host/30-storage check)" "snippets"

  export KEEL_MODE="yes"
  mod_rc host/30-storage apply >/dev/null
  assert_ran "pvesm set local --content backup,iso,snippets,vztmpl"
}

test_storage_already_correct() {
  _fake_storage_cfg
  stub_commands pvesm
  printf '{ "storages": [ { "name": "local", "content": ["backup","iso","vztmpl"] } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  assert_eq "$(mod_rc host/30-storage check)" "0" "порядок в списке не должен считаться расхождением"
}

test_storage_creates_dir_storage() {
  _fake_storage_cfg
  stub_commands pvesm mkdir
  printf '{ "storages": [ { "name": "media", "type": "dir", "path": "/mnt/media", "content": ["iso"] } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  assert_eq "$(mod_rc host/30-storage check)" "10"

  export KEEL_MODE="yes"
  mod_rc host/30-storage apply >/dev/null
  assert_ran "pvesm add dir media --path /mnt/media --content iso"
}

test_storage_refuses_to_invent_lvm() {
  _fake_storage_cfg
  stub_commands pvesm
  printf '{ "storages": [ { "name": "tank", "content": ["images"] } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
  mod_rc host/30-storage apply >/dev/null
  assert_not_ran "pvesm add"
}

test_storage_ignores_unlisted() {
  _fake_storage_cfg
  stub_commands pvesm
  printf '{ "storages": [ { "name": "local", "content": ["backup","iso","vztmpl"] } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
  mod_rc host/30-storage apply >/dev/null
  assert_not_ran "local-lvm"
}

# --- Модуль резервного копирования -------------------------------------------

_manifest_backup() {
  cat >"${T}/m.json" <<'EOF'
{
  "backup": {
    "schedule": "02:00",
    "storage": "local",
    "mode": "snapshot",
    "guests": [100, 101],
    "keep_last": 3
  }
}
EOF
  config_load "${T}/m.json"
}

test_backup_zero_rule() {
  stub_commands pvesh
  config_load "${T}/нет.json"
  assert_eq "$(mod_rc host/40-backup-jobs check)" "20"
}

test_backup_creates_job() {
  stub_commands pvesh
  stub_says "pvesh.get" <<<'[]'
  _manifest_backup
  assert_eq "$(mod_rc host/40-backup-jobs check)" "10" "задания нет — надо создать"

  export KEEL_MODE="yes"
  mod_rc host/40-backup-jobs apply >/dev/null
  assert_ran "pvesh create /cluster/backup"
  assert_ran "--schedule 02:00"
  assert_ran "--vmid 100,101"
  assert_ran "--prune-backups keep-last=3"
  assert_ran "--comment keel"
}

test_backup_updates_existing_job() {
  stub_commands pvesh
  stub_says "pvesh.get" <<<'[{"id":"backup-abc","comment":"keel","schedule":"04:00","storage":"local","mode":"snapshot","vmid":"100,101"}]'
  _manifest_backup
  assert_eq "$(mod_rc host/40-backup-jobs check)" "10" "расписание отличается"
  assert_contains "$(mod_out host/40-backup-jobs check)" "04:00"

  export KEEL_MODE="yes"
  mod_rc host/40-backup-jobs apply >/dev/null
  assert_ran "pvesh set /cluster/backup/backup-abc"
}

test_backup_matching_job_is_left_alone() {
  stub_commands pvesh
  stub_says "pvesh.get" <<<'[{"id":"backup-abc","comment":"keel","schedule":"02:00","storage":"local","mode":"snapshot","vmid":"100,101"}]'
  _manifest_backup
  assert_eq "$(mod_rc host/40-backup-jobs check)" "0" "совпадающее задание не трогаем"
}

test_backup_ignores_foreign_jobs() {
  stub_commands pvesh
  stub_says "pvesh.get" <<<'[{"id":"backup-чужое","comment":"сделано руками","schedule":"05:00","storage":"pbs"}]'
  _manifest_backup
  assert_eq "$(mod_rc host/40-backup-jobs check)" "10" "чужое задание не считается нашим"
  export KEEL_MODE="yes"
  mod_rc host/40-backup-jobs apply >/dev/null
  assert_ran "pvesh create /cluster/backup"
  assert_not_ran "pvesh set"
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

printf '\nФаза 1: хост\n'
it "updates: правило нуля"                      test_updates_zero_rule
it "updates: обновлять нечего"                  test_updates_nothing_to_do
it "updates: ставит dist-upgrade"               test_updates_applies_dist_upgrade
it "storage: чтение storage.cfg"                test_storage_reads_config
it "storage: доводит content до описанного"     test_storage_adds_missing_content
it "storage: порядок в списке не важен"         test_storage_already_correct
it "storage: создаёт dir-хранилище"             test_storage_creates_dir_storage
it "storage: не выдумывает LVM"                 test_storage_refuses_to_invent_lvm
it "storage: не трогает чужие хранилища"        test_storage_ignores_unlisted
it "backup: правило нуля"                       test_backup_zero_rule
it "backup: создаёт задание"                    test_backup_creates_job
it "backup: обновляет своё задание"             test_backup_updates_existing_job
it "backup: совпадающее не трогает"             test_backup_matching_job_is_left_alone
it "backup: не присваивает чужие задания"       test_backup_ignores_foreign_jobs

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
