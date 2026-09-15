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

# Каждый тест выполняется в своей подоболочке, поэтому exit гасит
# именно его. Раньше здесь был return, и упавшая проверка не мешала
# тесту досчитаться до конца и отчитаться об успехе.
fail() { printf 'ОШИБКА: %s\n' "$*" >&2; exit 1; }

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
  # shellcheck source=../lib/guests.sh
  source "${KEEL_ROOT}/lib/guests.sh"
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

test_config_fallback_parser() {
  # Путь на случай хоста, где relaxed повёл себя не так, как обещает
  # документация: комментарии срезаются нашим разбором.
  export KEEL_JSON_RELAXED="no"
  cat >"${T}/m.json" <<'EOF'
{
  # решётка
  "host": { "repos": "no-subscription" },   // две косые
  "storages": [
    { "name": "запятая, и скобка }" },
    { "name": "https://example.com/#anchor" },
  ],
}
EOF
  config_load "${T}/m.json"
  assert_eq "$(config_get host.repos)" "no-subscription" "комментарии обоих видов"
  assert_eq "$(config_get storages.0.name)" "запятая, и скобка }" "строка со спецсимволами цела"
  assert_eq "$(config_get storages.1.name)" "https://example.com/#anchor" "ссылка не обрезана"
  assert_eq "$(config_len storages)" "2" "висячие запятые убраны"
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


# --- Фаза 2: гости -----------------------------------------------------------

# Хост, на котором уже всё готово для создания гостей
_fake_guest_host() {
  export KEEL_FS_ROOT="${T}/root"
  mkdir -p "${KEEL_FS_ROOT}/etc/pve/qemu-server" "${KEEL_FS_ROOT}/etc/pve/lxc" \
           "${KEEL_FS_ROOT}/dev/dri" "${T}/vz/snippets"
  cat >"${KEEL_FS_ROOT}/etc/pve/storage.cfg" <<EOF
dir: local
	path ${T}/vz
	content iso,vztmpl,backup,snippets

lvmthin: local-lvm
	thinpool data
	vgname pve
	content rootdir,images
EOF
  : >"${KEEL_FS_ROOT}/dev/dri/renderD128"
  : >"${KEEL_FS_ROOT}/dev/dri/card0"
  stub_commands qm pct pveam curl xz
  stub_says "pct.help" <<'EOF'
  --dev[n] [path=]<Path>
EOF
  stub_says "pveam.available" <<'EOF'
system          ubuntu-24.04-standard_24.04-2_amd64.tar.zst
EOF
}

# Положить готовый образ в кэш, чтобы не изображать скачивание
_seed_image_cache() {
  mkdir -p "${KEEL_STATE_DIR}/images"
  : >"${KEEL_STATE_DIR}/images/$1"
}

test_guests_profile_resolution() {
  cat >"${T}/m.json" <<'EOF'
{ "guests": [
  { "id": 100, "name": "a", "profile": "desktop", "graphics": "dri" },
  { "id": 101, "name": "b", "profile": "desktop", "graphics": "virgl" },
  { "id": 102, "name": "c", "profile": "haos" }
] }
EOF
  config_load "${T}/m.json"
  assert_eq "$(guest_profile_name 0)" "desktop-lxc" "dri — это контейнер"
  assert_eq "$(guest_profile_name 1)" "desktop-vm"  "virgl — это ВМ"
  assert_eq "$(guest_profile_name 2)" "haos"        "обычный профиль как есть"
}

test_guests_disk_units() {
  assert_eq "$(disk_to_gb 64G)" "64"
  assert_eq "$(disk_to_gb 32)" "32"
  assert_eq "$(disk_to_gb 65536M)" "64"
}

test_guests_all_profiles_are_valid() {
  local name kind
  while IFS= read -r name; do
    profile_load "$name" || fail "профиль ${name} не читается"
    kind=$(prof_get kind "")
    case "$kind" in
      vm-image|vm-cloudinit|lxc) ;;
      *) fail "профиль ${name}: непонятный kind «${kind}»" ;;
    esac
    [[ -n "$(prof_get title "")" ]] || fail "профиль ${name}: нет title"
  done < <(profile_list)
}

test_guests_existing_is_never_touched() {
  _fake_guest_host
  printf '{ "guests": [ { "id": 100, "name": "haos", "profile": "haos" } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  printf 'cores: 2\nmemory: 4096\n' >"${KEEL_FS_ROOT}/etc/pve/qemu-server/100.conf"

  assert_eq "$(mod_rc guests/50-guests check)" "0" "существующий гость — работы нет"
  assert_contains "$(mod_out guests/50-guests check)" "не трогаю"

  export KEEL_MODE="yes"
  mod_rc guests/50-guests apply >/dev/null
  assert_not_ran "qm create"
  assert_not_ran "qm destroy"
}

test_guests_reports_drift_without_fixing() {
  _fake_guest_host
  printf '{ "guests": [ { "id": 100, "name": "haos", "profile": "haos", "memory": 8192 } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  printf 'cores: 2\nmemory: 4096\n' >"${KEEL_FS_ROOT}/etc/pve/qemu-server/100.conf"

  assert_contains "$(mod_out guests/50-guests check)" "память: на хосте 4096, в манифесте 8192"
  export KEEL_MODE="yes"
  mod_rc guests/50-guests apply >/dev/null
  assert_not_ran "qm set 100 --memory"
}

test_guests_haos_vm_commands() {
  _fake_guest_host
  _seed_image_cache "haos_ova-14.2.qcow2"
  printf '{ "guests": [ { "id": 100, "name": "haos", "profile": "haos", "storage": "local-lvm", "disk": "32G", "start_on_boot": true } ] }' >"${T}/m.json"
  config_load "${T}/m.json"

  assert_eq "$(mod_rc guests/50-guests check)" "10" "гостя нет — надо создавать"
  export KEEL_MODE="yes"
  mod_rc guests/50-guests apply >/dev/null

  assert_ran "qm create 100 --name haos"
  assert_ran "--machine q35"
  assert_ran "--bios ovmf"
  assert_ran "--efidisk0 local-lvm:0,efitype=4m,pre-enrolled-keys=0"
  assert_ran "import-from=${KEEL_STATE_DIR}/images/haos_ova-14.2.qcow2"
  assert_ran "qm set 100 --boot order=scsi0"
  assert_ran "qm resize 100 scsi0 32G"
  assert_ran "--onboot 1"
}

test_guests_cloudinit_vm_commands() {
  _fake_guest_host
  _seed_image_cache "noble-server-cloudimg-amd64.img"
  cat >"${T}/m.json" <<'EOF'
{ "guests": [ {
  "id": 101, "name": "desktop", "profile": "desktop", "graphics": "virgl",
  "storage": "local-lvm", "disk": "64G",
  "cloudinit": { "user": "alex", "ipconfig": "ip=dhcp" },
  "packages": ["obs-studio"]
} ] }
EOF
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
  mod_rc guests/50-guests apply >/dev/null

  assert_ran "qm create 101 --name desktop"
  assert_ran "--vga virtio-gl"
  assert_ran "--audio0 device=ich9-intel-hda,driver=spice"
  assert_ran "qm set 101 --ide2 local-lvm:cloudinit"
  assert_ran "qm set 101 --cicustom user=local:snippets/keel-101-user.yml"
  assert_ran "qm set 101 --ipconfig0 ip=dhcp"

  local snippet="${T}/vz/snippets/keel-101-user.yml"
  [[ -f "$snippet" ]] || fail "сниппет cloud-init не записан"
  assert_contains "$(cat "$snippet")" "#cloud-config"
  assert_contains "$(cat "$snippet")" "name: alex"
  assert_contains "$(cat "$snippet")" "- vlc"          # из профиля
  assert_contains "$(cat "$snippet")" "- obs-studio"   # из манифеста
  assert_contains "$(cat "$snippet")" "systemctl set-default graphical.target"
}

test_guests_cloudinit_needs_snippets_storage() {
  _fake_guest_host
  _seed_image_cache "noble-server-cloudimg-amd64.img"
  # Хранилище без snippets — создавать ВМ нельзя, и это должно быть сказано прямо
  cat >"${KEEL_FS_ROOT}/etc/pve/storage.cfg" <<EOF
dir: local
	path ${T}/vz
	content iso,vztmpl,backup
EOF
  printf '{ "guests": [ { "id": 101, "name": "d", "profile": "desktop", "graphics": "virgl" } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
  local out; out=$(mod_out guests/50-guests apply)
  assert_contains "$out" "сниппеты"
  assert_not_ran "qm create"
}

test_guests_lxc_desktop_commands() {
  _fake_guest_host
  cat >"${T}/m.json" <<'EOF'
{ "guests": [ {
  "id": 102, "name": "desktop", "profile": "desktop", "graphics": "dri",
  "storage": "local-lvm", "disk": "64G", "cores": 4, "memory": 8192,
  "cloudinit": { "user": "alex" }
} ] }
EOF
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
  mod_rc guests/50-guests apply >/dev/null

  assert_ran "pveam download local ubuntu-24.04-standard_24.04-2_amd64.tar.zst"
  assert_ran "pct create 102 local:vztmpl/ubuntu-24.04-standard_24.04-2_amd64.tar.zst"
  assert_ran "--rootfs local-lvm:64"
  assert_ran "--unprivileged 1"
  assert_ran "--features nesting=1"
  assert_ran "renderD128,gid=993"
  assert_ran "card0,gid=44"
  assert_ran "pct start 102"
  assert_ran "pct exec 102 -- bash /root/keel-post-install.sh"
}

test_guests_lxc_password_never_in_log() {
  _fake_guest_host
  printf '{ "guests": [ { "id": 102, "name": "d", "profile": "desktop", "graphics": "dri", "cloudinit": { "user": "alex" } } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
  local out; out=$(mod_out guests/50-guests apply)

  local secret_file="${KEEL_STATE_DIR}/secrets/102.txt"
  [[ -f "$secret_file" ]] || fail "пароль не сохранён в ${secret_file}"
  local pw; pw=$(cat "$secret_file")
  [[ -n "$pw" ]] || fail "пароль пустой"
  assert_eq "$(stat -c %a "$secret_file")" "600" "файл с паролем должен быть доступен только root"

  if printf '%s' "$out" | grep -qF -- "$pw"; then
    fail "пароль попал на экран"
  fi
  if grep -rqF -- "$pw" "$KEEL_LOG_DIR" 2>/dev/null; then
    fail "пароль попал в лог"
  fi
  assert_contains "$out" "********"
}


# --- Фаза 3: копия конфигурации хоста ----------------------------------------

_fake_host_etc() {
  export KEEL_FS_ROOT="${T}/root"
  mkdir -p "${KEEL_FS_ROOT}/etc/pve/qemu-server" "${KEEL_FS_ROOT}/etc/network" \
           "${KEEL_FS_ROOT}/etc/apt/sources.list.d" "${KEEL_FS_ROOT}/root/.ssh"
  printf 'version 8\n' >"${KEEL_FS_ROOT}/etc/pve/.version"
  printf 'cores: 2\n' >"${KEEL_FS_ROOT}/etc/pve/qemu-server/100.conf"
  printf 'auto vmbr0\niface vmbr0 inet static\n' >"${KEEL_FS_ROOT}/etc/network/interfaces"
  printf 'proxmox\n' >"${KEEL_FS_ROOT}/etc/hostname"
  printf 'ssh-ed25519 AAAA test\n' >"${KEEL_FS_ROOT}/root/.ssh/authorized_keys"
}

_manifest_config_backup() {
  cat >"${T}/m.json" <<EOF
{ "host": { "config_backup": { "path": "${T}/copies", "keep": 3, "max_age_hours": 24 } } }
EOF
  config_load "${T}/m.json"
}

test_cfgbackup_zero_rule() {
  _fake_host_etc
  config_load "${T}/нет.json"
  assert_eq "$(mod_rc host/90-config-backup check)" "20"
}

test_cfgbackup_creates_archive() {
  _fake_host_etc
  _manifest_config_backup
  assert_eq "$(mod_rc host/90-config-backup check)" "10" "копий ещё нет"

  export KEEL_MODE="yes"
  mod_rc host/90-config-backup apply >/dev/null

  local archive
  archive=$(find "${T}/copies" -name 'keel-host-*.tar.gz' | head -n1)
  [[ -n "$archive" ]] || fail "архив не создан"

  local listing; listing=$(tar -tzf "$archive")
  assert_contains "$listing" "etc/pve/qemu-server/100.conf"
  assert_contains "$listing" "etc/network/interfaces"
  assert_contains "$listing" "root/.ssh/authorized_keys"
  assert_contains "$listing" "host-report.txt"
  assert_contains "$listing" "manifest.json"
}

test_cfgbackup_fresh_copy_is_enough() {
  _fake_host_etc
  _manifest_config_backup
  export KEEL_MODE="yes"
  mod_rc host/90-config-backup apply >/dev/null
  assert_eq "$(mod_rc host/90-config-backup check)" "0" "свежая копия есть — работы нет"
  assert_eq "$(mod_rc host/90-config-backup verify)" "0"
}

test_cfgbackup_stale_copy_triggers_new_one() {
  _fake_host_etc
  _manifest_config_backup
  mkdir -p "${T}/copies"
  : >"${T}/copies/keel-host-2020-01-01_000000.tar.gz"
  touch -d '10 days ago' "${T}/copies/keel-host-2020-01-01_000000.tar.gz"
  assert_eq "$(mod_rc host/90-config-backup check)" "10" "старая копия — нужна новая"
}

test_cfgbackup_prunes_old_copies() {
  _fake_host_etc
  _manifest_config_backup
  mkdir -p "${T}/copies"
  local i
  for i in 1 2 3 4 5; do
    : >"${T}/copies/keel-host-old-${i}.tar.gz"
    touch -d "${i} days ago" "${T}/copies/keel-host-old-${i}.tar.gz"
  done
  export KEEL_MODE="yes"
  mod_rc host/90-config-backup apply >/dev/null

  local left; left=$(find "${T}/copies" -name 'keel-host-*.tar.gz' | wc -l)
  assert_eq "$left" "3" "должно остаться ровно keep копий"
  [[ -f "${T}/copies/keel-host-old-5.tar.gz" ]] && fail "самая старая копия должна была удалиться"
  return 0
}


test_guests_lxc_fixes_dri_permissions_itself() {
  _fake_guest_host
  printf '{ "guests": [ { "id": 102, "name": "d", "profile": "desktop", "graphics": "dri", "cloudinit": { "user": "alex" } } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  profile_load desktop-lxc

  local script; script=$(_lxc_post_install_script 0 alex секрет)
  # Номер группы внутри контейнера угадывать нельзя: в Ubuntu render=993,
  # в Debian 104. Сценарий должен смотреть на фактического владельца.
  assert_contains "$script" 'gid=$(stat -c %g "$dev")'
  assert_contains "$script" "groupadd -g"
  assert_contains "$script" "adduser KEEL_USER"
  assert_contains "$script" "xrdp"
}

test_modules_run_in_numeric_order() {
  # Гости создаются после хранилищ, а копия конфигурации — последней.
  # Порядок задаёт числовой префикс имени, а не каталог, в котором
  # модуль лежит: иначе guests/ оказывался бы раньше host/ по алфавиту.
  local order
  order=$(modules_list_ids | cut -f1 | paste -sd' ' -)
  assert_eq "$order" \
    "host/10-repos host/20-updates host/30-storage host/40-backup-jobs guests/50-guests host/90-config-backup"
}

# --- Командная строка --------------------------------------------------------

_keel() { "${KEEL_ROOT}/bin/keel" --plain "$@"; }

test_cli_basic_commands_work() {
  _keel version >/dev/null || fail "keel version упал"
  _keel help >/dev/null || fail "keel help упал"
  _keel modules >/dev/null || fail "keel modules упал"
  assert_contains "$(_keel modules)" "host/10-repos"
  assert_contains "$(_keel modules)" "guests/50-guests"
}

test_cli_validates_example_manifest() {
  _keel validate --manifest "${KEEL_ROOT}/manifest/host.example.json" >/dev/null \
    || fail "пример манифеста не проходит проверку"
}

test_cli_rejects_unknown_command() {
  local rc=0
  _keel такой-команды-нет >/dev/null 2>&1 || rc=$?
  [[ "$rc" -ne 0 ]] || fail "неизвестная команда должна завершаться ошибкой"
}

test_cli_password_says_when_there_is_none() {
  local out rc=0
  out=$(_keel password 999 2>&1) || rc=$?
  [[ "$rc" -ne 0 ]] || fail "для несуществующего пароля нужен ненулевой код"
  assert_contains "$out" "999"
}

test_cli_apply_refuses_on_non_proxmox() {
  local out rc=0
  out=$(_keel apply 2>&1) || rc=$?
  [[ "$rc" -ne 0 ]] || fail "apply на не-Proxmox должен отказываться"
  assert_contains "$out" "KEEL_ALLOW_NON_PVE"
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
it "config: запасной разбор без relaxed"        test_config_fallback_parser

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

printf '\nФаза 2: гости\n'
it "guests: профиль по роли и графике"          test_guests_profile_resolution
it "guests: единицы размера диска"              test_guests_disk_units
it "guests: все профили валидны"                test_guests_all_profiles_are_valid
it "guests: существующего не трогает"           test_guests_existing_is_never_touched
it "guests: сообщает о расхождениях, не правит" test_guests_reports_drift_without_fixing
it "guests: команды создания HAOS"              test_guests_haos_vm_commands
it "guests: команды создания ВМ с cloud-init"   test_guests_cloudinit_vm_commands
it "guests: без snippets честно отказывается"   test_guests_cloudinit_needs_snippets_storage
it "guests: команды создания LXC с /dev/dri"    test_guests_lxc_desktop_commands
it "guests: пароль не утекает в лог и на экран" test_guests_lxc_password_never_in_log
it "guests: права на /dev/dri чинятся сами"     test_guests_lxc_fixes_dri_permissions_itself

printf '\nФаза 3: копия конфигурации хоста\n'
it "config-backup: правило нуля"                test_cfgbackup_zero_rule
it "config-backup: собирает архив"              test_cfgbackup_creates_archive
it "config-backup: свежая копия — работы нет"   test_cfgbackup_fresh_copy_is_enough
it "config-backup: устаревшая копия обновляется" test_cfgbackup_stale_copy_triggers_new_one
it "config-backup: прореживает старые"          test_cfgbackup_prunes_old_copies

printf '\nКомандная строка\n'
it "modules: порядок по числовому префиксу"      test_modules_run_in_numeric_order
it "cli: базовые команды работают"              test_cli_basic_commands_work
it "cli: пример манифеста проходит проверку"    test_cli_validates_example_manifest
it "cli: неизвестная команда — ошибка"          test_cli_rejects_unknown_command
it "cli: говорит, когда пароля нет"             test_cli_password_says_when_there_is_none
it "cli: apply отказывается вне Proxmox"        test_cli_apply_refuses_on_non_proxmox

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
