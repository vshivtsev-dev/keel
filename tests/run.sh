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
  # Все пути уводим во временный каталог: ни один тест не смеет притронуться
  # к настоящему /root/keel. За этим следит отдельная проверка ниже.
  export KEEL_HOME="${T}/home"
  export KEEL_LOG_DIR="${T}/log"
  export KEEL_STATE_DIR="${T}/state"
  export KEEL_BACKUP_DIR="${T}/state/backups"
  export KEEL_SECRETS_DIR="${T}/state/secrets"
  export KEEL_MANIFEST="${T}/home/host.json"
  export KEEL_COLOR="off"
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
  # shellcheck source=../lib/gpu.sh
  source "${KEEL_ROOT}/lib/gpu.sh"
  # shellcheck source=../lib/modules.sh
  source "${KEEL_ROOT}/lib/modules.sh"
  # shellcheck source=../lib/doctor.sh
  source "${KEEL_ROOT}/lib/doctor.sh"
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

# --- Окружение ---------------------------------------------------------------
#
# Допущения, на которых построен keel. В контейнере Debian 13 это прямой
# ответ на вопрос «а будет ли оно работать на Proxmox VE 9».

test_env_perl_json_pp() {
  config_parser_available || fail "нет perl с JSON::PP — читать манифест нечем"
}

test_env_relaxed_comments_and_commas() {
  # Ради этого и выбран JSON вместо YAML: парсер есть всегда, а relaxed
  # даёт комментарии. Если допущение неверно — знать об этом надо сразу.
  config_relaxed_supported || fail "JSON::PP не поддерживает relaxed на этой системе"

  printf '{\n# решётка\n"a": 1,\n// две косые\n"b": [1, 2,],\n}' >"${T}/m.json"
  config_load "${T}/m.json"
  assert_eq "$(config_get a)" "1" "комментарий через #"
  assert_eq "$(config_len b)" "2" "комментарий через // и висячие запятые"
}

test_env_utf8_locale_available() {
  # Без UTF-8 локали ${#s} считает байты, и весь вывод с русскими
  # подписями разъезжается по колонкам
  local padded; padded=$(pad "Проверка" 12)
  assert_eq "${#padded}" "12" "нет UTF-8 локали — вывод будет кривым"
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

# --- Одно согласие на весь план ----------------------------------------------
#
# Обещание режима once — спросить один раз. Проверяется с двух сторон: после
# согласия никто больше не спрашивает, а без согласия никто ничего не делает.
# Стрелять в ногу здесь можно ровно двумя способами, и оба ниже.

test_core_once_asks_nothing_after_plan_confirmed() {
  export KEEL_MODE="once"
  KEEL_CONFIRMED=1
  # Вызов этой заглушки и есть провал: о шаге спрашивать уже не должны
  ui_confirm_step() { : >"${T}/спросили"; printf 'abort'; }

  run "создать файл" touch "${T}/создан" >/dev/null
  [[ ! -e "${T}/спросили" ]] || fail "план подтверждён, а keel всё равно спросил о шаге"
  [[ -e "${T}/создан" ]] || fail "после согласия на план команда должна выполниться"
}

test_core_once_without_confirmation_still_asks() {
  export KEEL_MODE="once"
  KEEL_CONFIRMED=0
  # Заглушка пишет файл, а не переменную: run() зовёт её в подстановке, то есть
  # в подоболочке, и присваивание оттуда до теста не доживает
  ui_confirm_step() { : >"${T}/спросили"; printf 'skip'; }

  # stderr тоже в /dev/null: «Пропущено» — ожидаемый здесь ответ, и в отчёте
  # о тестах ему делать нечего
  run "создать файл" touch "${T}/не-должен-появиться" >/dev/null 2>&1
  [[ -e "${T}/спросили" ]] || fail "без подтверждения плана run() обязан спросить"
  [[ ! -e "${T}/не-должен-появиться" ]] || fail "ответ был skip, а команда выполнилась"
}

# Пустой ответ в пошаговом режиме значит «применить». Если /dev/tty не
# открывается, read оставляет ответ пустым — и без проверки keel применял бы
# всё подряд, не спросив ни разу. Ровно этот случай: контейнер, systemd, nohup.
test_core_step_without_terminal_aborts() {
  export KEEL_MODE="step"
  keel_have_tty() { return 1; }

  local rc=0
  ( run "создать файл" touch "${T}/не-должен-появиться" ) >/dev/null 2>&1 || rc=$?
  assert_rc 3 "$rc" "без терминала пошаговый режим обязан прерваться"
  [[ ! -e "${T}/не-должен-появиться" ]] || fail "без терминала команда выполнилась молча"
}

test_ui_confirm_plan_without_terminal_cancels() {
  keel_have_tty() { return 1; }
  assert_eq "$(ui_confirm_plan 2>/dev/null)" "cancel" \
    "без терминала — отказ, а не молчаливое согласие"
}

test_core_interactive_only_when_it_may_ask() {
  keel_have_tty() { return 0; }
  KEEL_MODE="once"; keel_interactive || fail "в режиме once спрашивать можно"
  KEEL_MODE="step"; keel_interactive || fail "в пошаговом режиме спрашивать можно"
  KEEL_MODE="yes";  ! keel_interactive || fail "при --yes спрашивать нельзя"
  KEEL_MODE="dry";  ! keel_interactive || fail "в режиме просмотра спрашивать не о чем"

  KEEL_MODE="once"; keel_have_tty() { return 1; }
  ! keel_interactive || fail "без терминала спрашивать не у кого"
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
  _seed_image_cache "haos_ova-18.2.qcow2"
  printf '{ "guests": [ { "id": 100, "name": "haos", "profile": "haos", "storage": "local-lvm", "disk": "32G", "start_on_boot": true } ] }' >"${T}/m.json"
  config_load "${T}/m.json"

  assert_eq "$(mod_rc guests/50-guests check)" "10" "гостя нет — надо создавать"
  export KEEL_MODE="yes"
  mod_rc guests/50-guests apply >/dev/null

  assert_ran "qm create 100 --name haos"
  assert_ran "--machine q35"
  assert_ran "--bios ovmf"
  assert_ran "--efidisk0 local-lvm:0,efitype=4m,pre-enrolled-keys=0"
  assert_ran "import-from=${KEEL_STATE_DIR}/images/haos_ova-18.2.qcow2"
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
  "cloudinit": { "user": "av", "ipconfig": "ip=dhcp" },
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
  assert_contains "$(cat "$snippet")" "name: av"
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
  "cloudinit": { "user": "av" }
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
  printf '{ "guests": [ { "id": 102, "name": "d", "profile": "desktop", "graphics": "dri", "cloudinit": { "user": "av" } } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
  local out; out=$(mod_out guests/50-guests apply)

  local secret_file="${KEEL_SECRETS_DIR}/102.txt"
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
  printf '{ "guests": [ { "id": 102, "name": "d", "profile": "desktop", "graphics": "dri", "cloudinit": { "user": "av" } } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  profile_load desktop-lxc

  local script; script=$(_lxc_post_install_script 0 av секрет)
  # Номер группы внутри контейнера угадывать нельзя: в Ubuntu render=993,
  # в Debian 104. Сценарий должен смотреть на фактического владельца.
  assert_contains "$script" 'gid=$(stat -c %g "$dev")'
  assert_contains "$script" "groupadd -g"
  assert_contains "$script" "adduser KEEL_USER"
  assert_contains "$script" "xrdp"
}

test_modules_run_in_numeric_order() {
  # Гости создаются после хранилищ, проброс видеокарты — после гостей
  # (иначе отдавать её некому), копия конфигурации — последней.
  # Порядок задаёт числовой префикс имени, а не каталог, в котором
  # модуль лежит: иначе guests/ оказывался бы раньше host/ по алфавиту.
  local order
  order=$(modules_list_ids | cut -f1 | paste -sd' ' -)
  assert_eq "$order" \
    "host/10-repos host/20-updates host/30-storage host/40-backup-jobs guests/50-guests host/60-gpu-passthrough host/90-config-backup"
}

# Хост с манифестом, где ровно одному модулю есть что делать
_host_with_one_change() {
  _fake_host_deb822
  printf '{ "host": { "repos": "no-subscription" } }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="once"
}

_repos_file_written() {
  [[ -f "${KEEL_FS_ROOT}/etc/apt/sources.list.d/pve-no-subscription.sources" ]]
}

# «Нет» на плане значит «нет»: ни одного изменения на диске.
test_apply_cancelled_plan_touches_nothing() {
  _host_with_one_change
  ui_confirm_plan() { printf 'cancel'; }

  modules_apply host/10-repos >/dev/null 2>&1
  ! _repos_file_written || fail "после отказа от плана репозиторий всё равно записан"
}

# Ради чего всё затевалось: одно «да» — и дальше ни одного вопроса.
test_apply_one_confirmation_covers_the_whole_plan() {
  _host_with_one_change
  ui_confirm_plan() { printf 'apply'; }
  ui_confirm_step() { : >"${T}/спросили-о-шаге"; printf 'abort'; }

  modules_apply host/10-repos >/dev/null 2>&1
  [[ ! -e "${T}/спросили-о-шаге" ]] || fail "план подтверждён, а keel спросил ещё раз"
  _repos_file_written || fail "план подтверждён, а репозиторий не записан"
}

# Согласие живёт один прогон. Иначе второй «применить» из меню выполнился бы
# молча, опираясь на «да», сказанное совсем другому списку.
test_apply_confirmation_does_not_outlive_the_run() {
  _host_with_one_change
  ui_confirm_plan() { printf 'apply'; }

  modules_apply host/10-repos >/dev/null 2>&1
  assert_eq "$KEEL_CONFIRMED" "0" "согласие обязано сбрасываться после применения"
  assert_eq "$KEEL_MODE" "once" "режим обязан возвращаться после выбора «по шагам»"
}

# Менять нечего — вопроса быть не должно: спрашивать не о чем.
test_apply_asks_nothing_when_there_is_nothing_to_do() {
  _fake_host_deb822
  printf '{ "host": { "updates": false } }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="once"
  ui_confirm_plan() { : >"${T}/спросили-о-плане"; printf 'cancel'; }

  local out; out=$(modules_apply host/10-repos 2>&1)
  [[ ! -e "${T}/спросили-о-плане" ]] || fail "изменений нет, а keel всё равно спросил"
  assert_contains "$out" "Менять нечего"
}


# --- Проброс видеокарты (вариант C) ------------------------------------------

# Хост с одной видеокартой в своей IOMMU-группе — как на живом железе
_fake_gpu_host() {
  export KEEL_FS_ROOT="${T}/root"
  mkdir -p "${KEEL_FS_ROOT}/etc/default" "${KEEL_FS_ROOT}/etc/modprobe.d" \
           "${KEEL_FS_ROOT}/etc/pve/qemu-server" \
           "${KEEL_FS_ROOT}/sys/class/iommu/ivhd0" \
           "${KEEL_FS_ROOT}/sys/kernel/iommu_groups/16/devices"
  : >"${KEEL_FS_ROOT}/sys/kernel/iommu_groups/16/devices/0000:64:00.0"
  printf 'GRUB_CMDLINE_LINUX_DEFAULT="quiet"\nGRUB_TIMEOUT=5\n' \
    >"${KEEL_FS_ROOT}/etc/default/grub"
  printf '# /etc/modules\n' >"${KEEL_FS_ROOT}/etc/modules"
  printf 'cores: 4\nmemory: 8192\n' >"${KEEL_FS_ROOT}/etc/pve/qemu-server/201.conf"

  # cp и rm намеренно НЕ подменяем: ими пользуется откат, и проверять его
  # имеет смысл только с настоящими
  stub_commands lspci qm update-grub update-initramfs
  stub_says "lspci.-mm" <<'EOF'
64:00.0 "VGA compatible controller" "AMD" "Phoenix3" -rb3 -p00 "Unknown vendor" "Device 1001"
EOF
  stub_says "lspci.-k" <<'EOF'
64:00.0 VGA compatible controller: AMD Phoenix3
	Kernel driver in use: amdgpu
EOF
  stub_says "lspci.-n" <<'EOF'
64:00.0 0300: 1002:1900 (rev c1)
EOF
}

_manifest_gpu() {
  cat >"${T}/m.json" <<'EOF'
{ "host": { "gpu_passthrough": { "enabled": true, "device": "auto", "vm": 201 } } }
EOF
  config_load "${T}/m.json"
}

# Подтверждение с набором адреса: в тестах отвечаем за пользователя.
# Значение кладём в обычную переменную, а не в local: функция ui_input
# вызывается позже, когда local уже вышел из области видимости.
_answer_confirm() {
  KEEL_TEST_ANSWER=$1
  ui_input() { printf '%s' "$KEEL_TEST_ANSWER"; }
}

# Параметры ядра зависят от производителя процессора, а тесты должны давать
# один и тот же результат на любой машине
_pretend_cpu() {
  KEEL_TEST_CPU=$1
  host_cpu_vendor() { printf '%s' "$KEEL_TEST_CPU"; }
}

test_gpu_zero_rule() {
  _fake_gpu_host
  config_load "${T}/нет.json"
  assert_eq "$(mod_rc host/60-gpu-passthrough check)" "20" "без ключа — ничего не делать"

  printf '{ "host": { "gpu_passthrough": { "enabled": false, "vm": 201 } } }' >"${T}/m.json"
  config_load "${T}/m.json"
  assert_eq "$(mod_rc host/60-gpu-passthrough check)" "20" "enabled=false — тоже ничего"
}

test_gpu_resolves_single_card() {
  _fake_gpu_host
  _manifest_gpu
  gpu_resolve_device auto || fail "не определилась единственная видеокарта"
  assert_eq "$KEEL_GPU_ADDR" "64:00.0"
  assert_eq "$KEEL_GPU_IDS" "1002:1900" "ID устройства для привязки к vfio-pci"
}

test_gpu_refuses_without_iommu() {
  _fake_gpu_host
  rm -rf "${KEEL_FS_ROOT}/sys/class/iommu"
  _manifest_gpu
  local out; out=$(mod_out host/60-gpu-passthrough check)
  assert_contains "$out" "IOMMU выключен"
  assert_eq "$(mod_rc host/60-gpu-passthrough check)" "1" "без IOMMU — отказ, а не попытка"
}

test_gpu_refuses_on_shared_group() {
  _fake_gpu_host
  # Подселяем соседа в ту же группу: пробрасывать придётся вместе с ним
  : >"${KEEL_FS_ROOT}/sys/kernel/iommu_groups/16/devices/0000:64:00.1"
  _manifest_gpu
  local out; out=$(mod_out host/60-gpu-passthrough check)
  assert_contains "$out" "делится с другими устройствами"
  assert_eq "$(mod_rc host/60-gpu-passthrough check)" "1"
}

test_gpu_apply_writes_everything() {
  _fake_gpu_host
  _manifest_gpu
  export KEEL_MODE="yes"
  _answer_confirm "64:00.0"
  _pretend_cpu AMD

  assert_eq "$(mod_rc host/60-gpu-passthrough check)" "10" "на чистом хосте есть что менять"
  local out rc=0
  out=$( source "${KEEL_ROOT}/modules/host/60-gpu-passthrough.sh"; mod_apply 2>&1 ) || rc=$?
  (( rc == 0 )) || fail "применение не удалось (код ${rc}):
${out}"

  # Параметры ядра добавлены к существующим, а не затёрли их
  local grub; grub=$(cat "${KEEL_FS_ROOT}/etc/default/grub")
  assert_contains "$grub" 'GRUB_CMDLINE_LINUX_DEFAULT="quiet amd_iommu=on iommu=pt"'
  assert_contains "$grub" "GRUB_TIMEOUT=5"

  local modules; modules=$(cat "${KEEL_FS_ROOT}/etc/modules")
  assert_contains "$modules" "vfio_pci"

  local conf; conf=$(cat "${KEEL_FS_ROOT}/etc/modprobe.d/keel-vfio.conf")
  assert_contains "$conf" "options vfio-pci ids=1002:1900"
  assert_contains "$conf" "softdep amdgpu pre: vfio-pci"
  assert_contains "$conf" "blacklist amdgpu"

  assert_ran "update-grub"
  assert_ran "update-initramfs -u -k all"
  assert_ran "qm set 201 --hostpci0 64:00.0,pcie=1"
}

test_gpu_apply_is_idempotent() {
  _fake_gpu_host
  _manifest_gpu
  export KEEL_MODE="yes"
  _answer_confirm "64:00.0"
  _pretend_cpu AMD
  ( source "${KEEL_ROOT}/modules/host/60-gpu-passthrough.sh"; mod_apply ) >/dev/null 2>&1
  # ВМ уже получила устройство — отражаем это в её конфиге
  printf 'hostpci0: 64:00.0,pcie=1\n' >>"${KEEL_FS_ROOT}/etc/pve/qemu-server/201.conf"
  assert_eq "$(mod_rc host/60-gpu-passthrough check)" "0" "повторный запуск: менять нечего"
}

test_gpu_refuses_without_confirmation() {
  _fake_gpu_host
  _manifest_gpu
  export KEEL_MODE="yes"
  _answer_confirm "не то слово"
  _pretend_cpu AMD

  ( source "${KEEL_ROOT}/modules/host/60-gpu-passthrough.sh"; mod_apply ) >/dev/null 2>&1
  [[ -f "${KEEL_FS_ROOT}/etc/modprobe.d/keel-vfio.conf" ]] \
    && fail "без подтверждения устройство не должно уходить от хоста"
  assert_not_ran "qm set 201 --hostpci0"
}

test_gpu_state_records_changes() {
  _fake_gpu_host
  _manifest_gpu
  export KEEL_MODE="yes"
  _answer_confirm "64:00.0"
  _pretend_cpu AMD
  ( source "${KEEL_ROOT}/modules/host/60-gpu-passthrough.sh"; mod_apply ) >/dev/null 2>&1

  local state; state=$(cat "$(gpu_state_file)")
  assert_eq "$(json_get "$state" device)" "64:00.0"
  assert_eq "$(json_get "$state" ids)" "1002:1900"
  assert_eq "$(json_get "$state" vm)" "201"
  # grub и modules существовали до нас — их правим; modprobe.d создаём
  assert_contains "$state" "etc/default/grub"
  assert_contains "$state" "etc/modprobe.d/keel-vfio.conf"
  [[ -n "$(json_get "$state" stamp)" ]] || fail "не записана метка резервных копий"
}

test_gpu_revert_restores_files() {
  _fake_gpu_host
  _manifest_gpu
  local grub_before; grub_before=$(cat "${KEEL_FS_ROOT}/etc/default/grub")
  local modules_before; modules_before=$(cat "${KEEL_FS_ROOT}/etc/modules")

  export KEEL_MODE="yes"
  _answer_confirm "64:00.0"
  _pretend_cpu AMD
  ( source "${KEEL_ROOT}/modules/host/60-gpu-passthrough.sh"; mod_apply ) >/dev/null 2>&1
  printf 'hostpci0: 64:00.0,pcie=1\n' >>"${KEEL_FS_ROOT}/etc/pve/qemu-server/201.conf"

  # Откат идёт по записи, а не по догадке: cp и rm здесь настоящие
  stub_commands lspci qm update-grub update-initramfs
  stub_says "lspci.-mm" <<'EOF'
64:00.0 "VGA compatible controller" "AMD" "Phoenix3" -rb3 -p00 "Unknown vendor" "Device 1001"
EOF
  stub_says "lspci.-n" <<'EOF'
64:00.0 0300: 1002:1900 (rev c1)
EOF
  gpu_revert >/dev/null 2>&1 || fail "откат не удался"

  assert_eq "$(cat "${KEEL_FS_ROOT}/etc/default/grub")" "$grub_before" "grub вернулся как был"
  assert_eq "$(cat "${KEEL_FS_ROOT}/etc/modules")" "$modules_before" "/etc/modules вернулся как был"
  [[ -f "${KEEL_FS_ROOT}/etc/modprobe.d/keel-vfio.conf" ]] \
    && fail "созданный файл привязки должен быть удалён"
  [[ -f "$(gpu_state_file)" ]] && fail "запись о пробросе должна исчезнуть"
  assert_ran "qm set 201 --delete hostpci0"
  return 0
}

test_gpu_revert_without_state_says_so() {
  _fake_gpu_host
  local rc=0
  gpu_revert >/dev/null 2>&1 || rc=$?
  assert_rc 1 "$rc" "откатывать нечего — это ошибка, а не тишина"
}

test_gpu_passthrough_profile_is_real() {
  cat >"${T}/m.json" <<'EOF'
{ "guests": [ { "id": 201, "name": "d", "profile": "desktop", "graphics": "passthrough" } ] }
EOF
  config_load "${T}/m.json"
  assert_eq "$(guest_profile_name 0)" "desktop-vm-gpu" "passthrough больше не заглушка"
  profile_load desktop-vm-gpu || fail "профиль не читается"
  assert_eq "$(prof_get vm.bios)" "ovmf" "для проброса нужен UEFI"
  prof_bool vm.efidisk || fail "UEFI без EFI-диска не загрузится"
  [[ -z "$(prof_get vm.vga "")" ]] || fail "встроенный VGA должен остаться по умолчанию"
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


# --- Разбор первого живого прогона ------------------------------------------
#
# Каждый тест ниже падал на коде, который поехал на хост, и сторожит место,
# где keel уже один раз ошибся.

# Выключенный .sources обязан остаться валидным для apt: пустая строка внутри
# файла начинает новую запись, а запись без Types делает файл битым целиком —
# и apt перестаёт читать вообще все источники.
test_repos_disabled_file_stays_valid_for_apt() {
  _fake_host_deb822
  _manifest_repos "no-subscription"
  export KEEL_MODE="yes"
  ( source "${KEEL_ROOT}/modules/host/10-repos.sh"; mod_apply ) >/dev/null 2>&1 \
    || fail "применение не удалось"

  local f="${KEEL_FS_ROOT}/etc/apt/sources.list.d/pve-enterprise.sources"
  if grep -q '^[[:space:]]*$' "$f"; then
    fail "пустая строка разрывает запись — apt сочтёт файл битым:
$(cat "$f")"
  fi
  assert_contains "$(cat "$f")" "Types: deb"
  assert_contains "$(cat "$f")" "Enabled: false"
}

# Файл, сломанный прошлой версией keel, должен опознаваться как невыключенный
# и чиниться при следующем применении — руками на хосте ничего не правим.
test_repos_repairs_file_broken_by_older_keel() {
  _fake_host_deb822
  local f="${KEEL_FS_ROOT}/etc/apt/sources.list.d/pve-enterprise.sources"
  printf '\n# Выключено keel\nEnabled: false\n' >>"$f"
  _manifest_repos "no-subscription"

  local rc=0
  ( source "${KEEL_ROOT}/modules/host/10-repos.sh"; mod_check ) >/dev/null 2>&1 || rc=$?
  assert_rc 10 "$rc" "битый файл нельзя считать выключенным"

  export KEEL_MODE="yes"
  ( source "${KEEL_ROOT}/modules/host/10-repos.sh"; mod_apply ) >/dev/null 2>&1 \
    || fail "починка не удалась"
  if grep -q '^[[:space:]]*$' "$f"; then
    fail "починка не убрала пустую строку:
$(cat "$f")"
  fi
  assert_eq "$(grep -c 'Enabled: false' "$f")" "1" "выключатель должен остаться один"

  rc=0
  ( source "${KEEL_ROOT}/modules/host/10-repos.sh"; mod_check ) >/dev/null 2>&1 || rc=$?
  assert_rc 0 "$rc" "после починки работы быть не должно"
}

# Когда apt не может прочитать источники, он молчит в stdout — и ноль строк
# «Inst» означал «всё обновлено». Теперь это ошибка, а не зелёная галочка.
test_updates_refuses_when_apt_is_broken() {
  stub_commands apt-get
  cat >"${T}/bin/apt-get" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "apt-get $*" >> "$KEEL_STUB_LOG"
printf 'E: Malformed stanza 2 in source list /etc/apt/sources.list.d/ceph.sources (type)\n' >&2
printf 'E: The list of sources could not be read.\n' >&2
exit 100
STUB
  chmod +x "${T}/bin/apt-get"

  printf '{ "host": { "updates": true } }' >"${T}/m.json"
  config_load "${T}/m.json"

  assert_eq "$(mod_rc host/20-updates check)" "1" "битые источники — ошибка, а не «обновлять нечего»"
  assert_contains "$(mod_out host/20-updates check)" "не может прочитать списки источников"
  assert_eq "$(mod_rc host/20-updates apply)" "1" "применение обязано отказаться"
  assert_not_ran "apt-get -y dist-upgrade"
}

# IFS=$'\n\t' в bin/keel склеивал "${массив[*]}" переводами строк, и список
# пакетов рассыпался по строкам: под set -e вторая строка убивала настройку
# контейнера. Стенд этого не замечал, потому что сам bin/keel не запускает, —
# поэтому здесь IFS портится нарочно.
test_lxc_install_line_keeps_packages_together() {
  local IFS=$'\n\t'
  _fake_guest_host
  printf '{ "guests": [ { "id": 102, "name": "d", "profile": "desktop", "graphics": "dri", "cloudinit": { "user": "av" }, "packages": ["mc"] } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
  local out; out=$(mod_out guests/50-guests apply)

  local line; line=$(printf '%s\n' "$out" | grep -m1 'apt-get install')
  [[ -n "$line" ]] || fail "в сценарии нет установки пакетов:
${out}"
  assert_contains "$line" "xfce4"
  assert_contains "$line" "mc" "пакет из манифеста должен попасть в ту же строку"
  # Голое имя пакета отдельной строкой — это команда, которой нет
  if printf '%s\n' "$out" | grep -qx '[[:space:]]*xfce4-goodies'; then
    fail "пакеты разъехались по строкам — сценарий не выполнится:
${out}"
  fi
}

# Просмотр плана не создаёт ничего, включая файл с паролем.
test_dry_run_creates_no_secret_file() {
  _fake_guest_host
  printf '{ "guests": [ { "id": 102, "name": "d", "profile": "desktop", "graphics": "dri", "cloudinit": { "user": "av" } } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="dry"
  mod_out guests/50-guests apply >/dev/null

  local secret_file="${KEEL_SECRETS_DIR}/102.txt"
  if [[ -e "$secret_file" ]]; then
    fail "просмотр плана создал ${secret_file} — он обязан ничего не менять"
  fi
  assert_not_ran "pct create"
}

# «Ничего не удаляет» в шапке модуля должно быть правдой: типы content,
# которых нет в манифесте, но есть на хосте, обязаны уцелеть.
test_storage_keeps_content_types_it_did_not_add() {
  _fake_storage_cfg
  # На живом хосте у local был ещё и import — панель импорта дисков
  sed -i 's/content iso,vztmpl,backup/content iso,vztmpl,backup,import/' \
    "${KEEL_FS_ROOT}/etc/pve/storage.cfg"
  stub_commands pvesm
  printf '{ "storages": [ { "name": "local", "content": ["iso","vztmpl","backup","snippets"] } ] }' >"${T}/m.json"
  config_load "${T}/m.json"

  assert_eq "$(mod_rc host/30-storage check)" "10" "snippets не хватает"
  export KEEL_MODE="yes"
  mod_rc host/30-storage apply >/dev/null
  assert_ran "pvesm set local --content backup,import,iso,snippets,vztmpl"

  # И наоборот: лишний тип на хосте не повод считать, что есть работа
  printf '{ "storages": [ { "name": "local", "content": ["iso"] } ] }' >"${T}/m2.json"
  config_load "${T}/m2.json"
  assert_eq "$(mod_rc host/30-storage check)" "0" "лишний тип на хосте — не расхождение"
}


# Глобальный IFS — общий корень четырёх поломок сразу: он ломает "${массив[*]}"
# и словоделение по пробелам, от списка пакетов до выбора модулей в меню.
# Кавычки в проекте расставлены, отдельный IFS не нужен ни одной строке.
test_no_global_ifs() {
  local hits
  hits=$(grep -rn '^[[:space:]]*IFS=' bin/ lib/ modules/ 2>/dev/null \
         | grep -v 'local IFS=' || true)
  if [[ -n "$hits" ]]; then
    fail "глобальный IFS меняет разбор во всём проекте — задавай его рядом с местом использования:
${hits}"
  fi
}

# Интерфейс — текст, и не должен снова обзавестись рисовалкой окон.
#
# Проверка не про вкус. newt заливает фоном весь экран и рисует коробку
# фиксированных 78 колонок независимо от терминала, а главное — перерисовывает
# экран, унося наверх всё, что keel уже сказал. На IPMI-консоли, где идёт
# восстановление, это разница между «видно ход работы» и «видно последний кадр».
# Вернуть такое случайно легко, поэтому стоит сторож.
test_ui_draws_no_windows() {
  local hits
  # Строки комментариев отбрасываются: объяснить, почему рисовалки окон тут
  # нет, — ровно то, чего от комментария и ждут, и запрещать это незачем
  hits=$(grep -rniE 'whiptail|newt' bin/ lib/ modules/ install.sh 2>/dev/null \
         | grep -vE '^[^:]+:[0-9]+:[[:space:]]*#' || true)
  if [[ -n "$hits" ]]; then
    fail "интерфейс keel текстовый — рисовалка окон здесь лишняя:
${hits}"
  fi
}


# --- Один каталог, туннель и экран подтверждения -----------------------------

# Всё, с чем работает человек, лежит в KEEL_HOME и больше нигде. Раньше пути
# расходились по /opt, /var/lib и /var/log, и приходилось помнить три места.
test_paths_all_live_under_home() {
  local home="${T}/дом" out line
  out=$(env -u KEEL_LOG_DIR -u KEEL_STATE_DIR -u KEEL_BACKUP_DIR \
            -u KEEL_SECRETS_DIR -u KEEL_MANIFEST KEEL_HOME="$home" \
        bash -c 'source "$1"/lib/core.sh
                 printf "%s\n%s\n%s\n%s\n" \
                   "$KEEL_LOG_DIR" "$KEEL_STATE_DIR" "$KEEL_BACKUP_DIR" "$KEEL_SECRETS_DIR"' \
        _ "$KEEL_ROOT")
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    [[ "$line" == "${home}"* ]] || fail "путь ${line} уехал из ${home}"
  done <<< "$out"

  # И это не только значения переменных: настоящий запуск кладёт туда же
  env -u KEEL_LOG_DIR -u KEEL_STATE_DIR -u KEEL_BACKUP_DIR \
      -u KEEL_SECRETS_DIR -u KEEL_MANIFEST KEEL_HOME="$home" \
      "${KEEL_ROOT}/bin/keel" --plain version >/dev/null
  [[ -f "${home}/journal.tsv" ]] || fail "журнал не лёг в ${home}"
  compgen -G "${home}/logs/*.log" >/dev/null || fail "лог не лёг в ${home}/logs"
}

# Туннель без токена — это не туннель. Придумать токен keel не может,
# поэтому обязан отказаться, а не сделать вид, что справился.
test_cloudflared_refuses_without_token() {
  _fake_guest_host
  stub_says "pveam.available" <<'EOF'
system          debian-13-standard_13.1-1_amd64.tar.zst
EOF
  printf '{ "guests": [ { "id": 102, "name": "tunnel", "profile": "cloudflared" } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"

  assert_eq "$(mod_rc guests/50-guests apply)" "1" "без токена должен быть отказ"
  assert_contains "$(mod_out guests/50-guests apply)" "Нет токена"
  assert_not_ran "pct create"
}

# С токеном: он доходит до контейнера, но не появляется на экране.
test_cloudflared_installs_service_and_hides_token() {
  _fake_guest_host
  stub_says "pveam.available" <<'EOF'
system          debian-13-standard_13.1-1_amd64.tar.zst
EOF
  mkdir -p "$KEEL_SECRETS_DIR"
  printf 'eyJhIjoiСЕКРЕТНЫЙТОКЕН"\n' >"${KEEL_SECRETS_DIR}/cloudflared.txt"
  printf '{ "guests": [ { "id": 102, "name": "tunnel", "profile": "cloudflared" } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
  local out; out=$(mod_out guests/50-guests apply)

  assert_ran "pct create 102"
  assert_contains "$out" "cloudflared service install"
  if printf '%s' "$out" | grep -qF 'eyJhIjoiСЕКРЕТНЫЙТОКЕН"'; then
    fail "токен показан на экране"
  fi
  if grep -rqF 'eyJhIjoiСЕКРЕТНЫЙТОКЕН"' "$KEEL_LOG_DIR" 2>/dev/null; then
    fail "токен попал в лог"
  fi
  assert_contains "$out" "********"

  # Служебному контейнеру пользователь не нужен
  if printf '%s' "$out" | grep -q 'chpasswd'; then
    fail "туннелю завели пользователя с паролем — он там не нужен"
  fi
}

# Просмотр плана не спрашивает токен и не создаёт файлов.
test_cloudflared_dry_run_asks_nothing() {
  _fake_guest_host
  stub_says "pveam.available" <<'EOF'
system          debian-13-standard_13.1-1_amd64.tar.zst
EOF
  printf '{ "guests": [ { "id": 102, "name": "tunnel", "profile": "cloudflared" } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="dry"
  local out; out=$(mod_out guests/50-guests apply)

  assert_contains "$out" "будет запрошен при применении"
  [[ ! -e "${KEEL_SECRETS_DIR}/cloudflared.txt" ]] \
    || fail "просмотр плана создал файл с токеном"
  assert_not_ran "pct create"
}

# Сценарий первичной настройки уносит с собой пароль и токен — и не должен
# оставаться в контейнере после выполнения.
test_post_install_script_removes_itself() {
  _fake_guest_host
  printf '{ "guests": [ { "id": 102, "name": "d", "profile": "desktop", "graphics": "dri", "cloudinit": { "user": "av" } } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
  local out; out=$(mod_out guests/50-guests apply)

  local last
  last=$(printf '%s\n' "$out" | grep -E '^ +rm -f "\$0"' | tail -n1)
  [[ -n "$last" ]] || fail "сценарий не удаляет себя — пароль останется лежать в контейнере:
${out}"
}

# Длинный diff показывается целиком. Обрезать его было нужно, только пока он
# лез в окно фиксированной высоты; в потоке текста обрезка — это потерянные
# строки ровно там, где человек решает, применять ли изменение.
test_confirm_shows_the_whole_body() {
  keel_have_tty() { return 0; }
  # read вернёт единицу на закрытом stdin — для теста это ответ «пусто»,
  # то есть «применить», а нам важен сам текст, ушедший на экран
  local shown
  shown=$(ui_confirm_step "правка" "$(seq 1 40)" 2>&1 >/dev/null </dev/null)
  assert_contains "$shown" "40"
  assert_eq "$(printf '%s\n' "$shown" | grep -cE '^[0-9]+$')" "40" "все сорок строк"
}


# --- Проба связи перед обновлением -------------------------------------------
#
# Однажды apt на живом хосте дорос до 6.4 ГБ и едва не увёл машину в OOM:
# DNS отдавал IPv6, маршрута до него не было, IPv4 отдавал 4 КБ/с, а очередь
# закачек на сотню пакетов копилась в памяти. Тесты ниже стерегут это место.

# Хост, где apt-get отвечает как настоящий, а curl — с заданной скоростью
_fake_slow_net() {
  local speed=$1
  stub_commands apt-get curl
  stub_says "apt-get.-s" <<'EOF'
Inst libc6 [2.41-12] (2.41-13 Debian:13 [amd64])
Inst openssl [3.5.6] (3.5.7 Debian:13 [amd64])
EOF
  stub_says "apt-get.--print-uris" <<'EOF'
'http://deb.debian.org/debian/pool/main/g/glibc/libc6_2.41-13_amd64.deb' libc6_2.41-13_amd64.deb 2851234 SHA256:aaa
'http://deb.debian.org/debian/pool/main/o/openssl/openssl_3.5.7_amd64.deb' openssl_3.5.7_amd64.deb 1400000 SHA256:bbb
EOF
  stub_says "curl" <<EOF
${speed}
EOF
  printf '{ "host": { "updates": true } }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
}

test_updates_refuses_on_slow_network() {
  _fake_slow_net 4094          # ровно та скорость, что была на живом хосте
  local out; out=$(mod_out host/20-updates apply)

  assert_eq "$(mod_rc host/20-updates apply)" "1" "на 4 КБ/с начинать нельзя"
  assert_contains "$out" "этого мало"
  assert_contains "$out" "3 КБ/с"          # 4094 Б/с округляется вниз
  assert_not_ran "apt-get -y dist-upgrade"
}

test_updates_refuses_when_repo_silent() {
  _fake_slow_net 0             # curl не достучался
  local out; out=$(mod_out host/20-updates apply)

  assert_eq "$(mod_rc host/20-updates apply)" "1" "молчащий репозиторий — не повод качать"
  assert_contains "$out" "Репозиторий не отвечает"
  assert_contains "$out" "libc6_2.41-13_amd64.deb"   # назвали, что именно не открылось
  assert_not_ran "apt-get -y dist-upgrade"
}

test_updates_proceeds_on_good_network() {
  _fake_slow_net 5000000
  mod_rc host/20-updates apply >/dev/null
  assert_ran "apt-get -y dist-upgrade"
}

test_updates_speed_check_can_be_switched_off() {
  _fake_slow_net 4094
  printf '{ "host": { "updates": true, "updates_min_speed": "0" } }' >"${T}/m.json"
  config_load "${T}/m.json"
  mod_rc host/20-updates apply >/dev/null
  assert_ran "apt-get -y dist-upgrade"
  assert_not_ran "curl"        # пробы не было вовсе
}

test_updates_no_probe_when_nothing_to_download() {
  stub_commands apt-get curl
  stub_says "apt-get.-s" <<'EOF'
Inst libc6 [2.41-12] (2.41-13 Debian:13 [amd64])
EOF
  # --print-uris молчит: всё уже скачано в кэш
  printf '{ "host": { "updates": true } }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"

  mod_rc host/20-updates apply >/dev/null
  assert_not_ran "curl"
  assert_ran "apt-get -y dist-upgrade"
}

test_apt_calls_carry_limits() {
  _fake_slow_net 5000000
  mod_rc host/20-updates apply >/dev/null
  assert_ran "Acquire::Retries=1"
  assert_ran "Acquire::http::Timeout=30"
}

# Маршрута IPv6 нет, а DNS адреса отдаёт — именно этот случай и съел память.
test_doctor_warns_about_unreachable_ipv6() {
  stub_commands ip getent apt-get
  stub_says "apt-get.indextargets" <<'EOF'
deb.debian.org
EOF
  # ip -6 route show default молчит — маршрута нет; getent отвечает — AAAA есть
  local out; out=$(doctor_host 2>&1)
  assert_contains "$out" "DNS отдаёт AAAA"
  assert_contains "$out" "ForceIPv4"

  # А теперь маршрут есть — предупреждать не о чем
  stub_says "ip" <<'EOF'
default via fe80::1 dev vmbr0 metric 1024
EOF
  out=$(doctor_host 2>&1)
  assert_contains "$out" "маршрут по умолчанию есть"
  if printf '%s' "$out" | grep -q "ForceIPv4"; then
    fail "маршрут есть, а keel всё равно советует ForceIPv4"
  fi
}


# --- Образ по факту, а не по шаблону -----------------------------------------
#
# На живом хосте keel спросил у GitHub последнюю версию (18.3), собрал адрес по
# шаблону и упёрся в 404: файла с таким именем наверху не оказалось. 404 — это
# определённый ответ «такого нет», а не сбой связи, и повторять его бессмысленно.

# curl, который отвечает отказом на одни адреса и согласием на другие.
# Первый аргумент keel'овского url_exists — флаги, поэтому решение принимаем
# по последнему аргументу: это и есть проверяемый адрес.
_curl_says_404_for() {
  local bad=$1
  stub_commands curl
  cat >"${T}/bin/curl" <<STUB
#!/usr/bin/env bash
printf '%s\n' "curl \$*" >> "\$KEEL_STUB_LOG"
for a in "\$@"; do url="\$a"; done
case "\$url" in
  *${bad}*) exit 22 ;;
esac
if [[ -f "\$KEEL_STUB_OUT/curl" ]]; then cat "\$KEEL_STUB_OUT/curl"; fi
exit 0
STUB
  chmod +x "${T}/bin/curl"
}

test_image_falls_back_when_version_missing() {
  _fake_guest_host
  _curl_says_404_for "18.9"
  _seed_image_cache "haos_ova-18.2.qcow2"
  stub_says "curl" <<'EOF'
{ "tag_name": "18.9", "assets": [] }
EOF
  printf '{ "guests": [ { "id": 100, "name": "haos", "profile": "haos", "storage": "local-lvm" } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
  local out; out=$(mod_out guests/50-guests apply)

  assert_contains "$out" "беру запасную 18.2"
  assert_ran "haos_ova-18.2.qcow2"
  assert_ran "qm create 100"
}

test_image_names_every_url_it_tried() {
  _fake_guest_host
  _curl_says_404_for "haos_ova"        # ни один адрес не живой
  stub_says "curl" <<'EOF'
{ "tag_name": "18.9", "assets": [] }
EOF
  printf '{ "guests": [ { "id": 100, "name": "haos", "profile": "haos" } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
  local out; out=$(mod_out guests/50-guests apply)

  assert_contains "$out" "Не нашёл ни одного живого адреса"
  assert_contains "$out" "haos_ova-18.9"      # назван и тот, что искали
  assert_contains "$out" "haos_ova-18.2"      # и запасной
  assert_not_ran "qm create"
}

test_image_takes_name_from_github_answer() {
  _fake_guest_host
  _seed_image_cache "haos_ova-19.0.qcow2"
  stub_says "curl" <<'EOF'
{ "tag_name": "19.0",
  "assets": [
    { "name": "haos_generic-x86-64-19.0.img.xz", "browser_download_url": "https://example.invalid/generic.img.xz" },
    { "name": "haos_ova-19.0.qcow2.xz", "browser_download_url": "https://example.invalid/haos_ova-19.0.qcow2.xz" }
  ] }
EOF
  printf '{ "guests": [ { "id": 100, "name": "haos", "profile": "haos" } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
  mod_rc guests/50-guests apply >/dev/null

  # Ссылка взята из ответа GitHub, а не собрана из шаблона
  assert_ran "https://example.invalid/haos_ova-19.0.qcow2.xz"
  assert_ran "qm create 100"
}

test_image_version_from_manifest_wins() {
  _fake_guest_host
  _seed_image_cache "haos_ova-17.5.qcow2"
  stub_says "curl" <<'EOF'
{ "tag_name": "19.0", "assets": [] }
EOF
  printf '{ "guests": [ { "id": 100, "name": "haos", "profile": "haos", "image_version": "17.5" } ] }' >"${T}/m.json"
  config_load "${T}/m.json"
  export KEEL_MODE="yes"
  mod_rc guests/50-guests apply >/dev/null

  assert_ran "haos_ova-17.5.qcow2"
  if stub_log | grep -q "haos_ova-19.0"; then
    fail "версия из манифеста должна перекрывать ответ GitHub"
  fi
}

# --- Выбор гостей ------------------------------------------------------------

_manifest_two_kinds() {
  cat >"${T}/m.json" <<'EOF'
{ "guests": [
  { "id": 100, "name": "haos",    "profile": "haos" },
  { "id": 101, "name": "desktop", "profile": "desktop", "graphics": "dri",
    "cloudinit": { "user": "av" } }
] }
EOF
  config_load "${T}/m.json"
}

test_guest_filter_creates_only_chosen() {
  _fake_guest_host
  _manifest_two_kinds
  export KEEL_MODE="yes" KEEL_GUESTS_ONLY="101"
  mod_rc guests/50-guests apply >/dev/null

  assert_ran "pct create 101"
  assert_not_ran "qm create"
}

test_guest_filter_says_who_was_skipped() {
  _fake_guest_host
  _manifest_two_kinds
  export KEEL_GUESTS_ONLY="101"
  local out; out=$(mod_out guests/50-guests check)
  assert_contains "$out" "пропущено по выбору"
  assert_contains "$out" "101"
}

test_guest_kind_splits_vm_and_lxc() {
  _fake_guest_host
  _manifest_two_kinds
  assert_eq "$(guest_kind 0)" "vm"  "haos — виртуальная машина"
  assert_eq "$(guest_kind 1)" "lxc" "рабочий стол — контейнер"
}


# Предупреждение о задании обязано давать выход, а не только констатацию:
# id, место в интерфейсе и готовую команду.
test_backup_warning_tells_how_to_remove() {
  stub_commands pvesh
  stub_says "pvesh.get" <<'EOF'
[{"all":1,"enabled":1,"id":"backup-чужое-0104","schedule":"sun 01:00","storage":"local","type":"vzdump"}]
EOF
  printf '{ "backup": { "schedule": "02:00", "storage": "local", "guests": [200, 201] } }' >"${T}/m.json"
  config_load "${T}/m.json"
  local out; out=$(mod_out host/40-backup-jobs check)

  assert_contains "$out" "backup-чужое-0104"
  assert_contains "$out" "pvesh delete /cluster/backup/backup-чужое-0104"
  assert_contains "$out" "Резервная копия"
}

# Ключ backup убрали, а задание keel осталось — про него надо сказать,
# но удалять молча нельзя.
test_backup_reports_orphan_job() {
  stub_commands pvesh
  stub_says "pvesh.get" <<'EOF'
[{"comment":"keel","enabled":1,"id":"d34ad502-keel","schedule":"02:00","storage":"local","type":"vzdump","vmid":"200,201"}]
EOF
  printf '{ "host": { "updates": false } }' >"${T}/m.json"
  config_load "${T}/m.json"

  assert_eq "$(mod_rc host/40-backup-jobs check)" "20" "без ключа модуль не работает"
  local out; out=$(mod_out host/40-backup-jobs check)
  assert_contains "$out" "задание keel на хосте осталось"
  assert_contains "$out" "pvesh delete /cluster/backup/d34ad502-keel"
  assert_not_ran "pvesh delete"
}

# --- Запуск ------------------------------------------------------------------

printf '\nОкружение\n'
it "env: perl с JSON::PP на месте"              test_env_perl_json_pp
it "env: relaxed — комментарии и запятые"       test_env_relaxed_comments_and_commas
it "env: UTF-8 локаль для выравнивания"         test_env_utf8_locale_available

# Не тесты, а справка: что именно за система под нами.
# Без выравнивания по колонкам — printf считает байты, а не символы.
printf '  · система: %s\n' "$(. /etc/os-release 2>/dev/null && echo "${PRETTY_NAME:-неизвестно}")"
printf '  · bash %s, perl %s\n' "${BASH_VERSION}" "$(perl -e 'print $]' 2>/dev/null || echo '—')"

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
it "core: все пути внутри KEEL_HOME"            test_paths_all_live_under_home
it "ui: длинный diff показывается целиком"      test_confirm_shows_the_whole_body

printf '\nСогласие: один вопрос вместо полусотни\n'
it "once: после согласия на план вопросов нет"  test_core_once_asks_nothing_after_plan_confirmed
it "once: без согласия ничего не выполняется"   test_core_once_without_confirmation_still_asks
it "step: без терминала — отказ, а не тишина"   test_core_step_without_terminal_aborts
it "план: без терминала вопрос отменяется"      test_ui_confirm_plan_without_terminal_cancels
it "core: спрашиваем только когда можно"        test_core_interactive_only_when_it_may_ask
it "apply: отказ от плана ничего не трогает"    test_apply_cancelled_plan_touches_nothing
it "apply: одно согласие на весь план"          test_apply_one_confirmation_covers_the_whole_plan
it "apply: согласие не переживает прогон"       test_apply_confirmation_does_not_outlive_the_run
it "apply: менять нечего — вопроса нет"         test_apply_asks_nothing_when_there_is_nothing_to_do

printf '\nМодуль репозиториев\n'
it "repos: правило нуля без манифеста"          test_repos_zero_rule_without_manifest
it "repos: применение и идемпотентность"        test_repos_apply_then_idempotent
it "repos: старый формат .list"                 test_repos_old_list_format
it "repos: отвергает неизвестное значение"      test_repos_rejects_unknown_value
it "repos: не трогает настоящую систему"        test_repos_never_touches_real_root_in_tests
it "repos: выключенный файл валиден для apt"    test_repos_disabled_file_stays_valid_for_apt
it "repos: чинит файл, сломанный прошлой версией" test_repos_repairs_file_broken_by_older_keel

printf '\nФаза 1: хост\n'
it "updates: правило нуля"                      test_updates_zero_rule
it "updates: обновлять нечего"                  test_updates_nothing_to_do
it "updates: ставит dist-upgrade"               test_updates_applies_dist_upgrade
it "updates: битые источники — ошибка"          test_updates_refuses_when_apt_is_broken
it "updates: медленная сеть — отказ"            test_updates_refuses_on_slow_network
it "updates: молчащий репозиторий — отказ"      test_updates_refuses_when_repo_silent
it "updates: нормальная сеть — качаем"          test_updates_proceeds_on_good_network
it "updates: проверку скорости можно выключить" test_updates_speed_check_can_be_switched_off
it "updates: качать нечего — пробы нет"         test_updates_no_probe_when_nothing_to_download
it "updates: у apt ограничены повторы и ожидание" test_apt_calls_carry_limits
it "doctor: предупреждает про IPv6 без маршрута" test_doctor_warns_about_unreachable_ipv6
it "storage: чтение storage.cfg"                test_storage_reads_config
it "storage: доводит content до описанного"     test_storage_adds_missing_content
it "storage: порядок в списке не важен"         test_storage_already_correct
it "storage: создаёт dir-хранилище"             test_storage_creates_dir_storage
it "storage: не выдумывает LVM"                 test_storage_refuses_to_invent_lvm
it "storage: не трогает чужие хранилища"        test_storage_ignores_unlisted
it "storage: не удаляет чужие типы content"     test_storage_keeps_content_types_it_did_not_add
it "backup: правило нуля"                       test_backup_zero_rule
it "backup: создаёт задание"                    test_backup_creates_job
it "backup: обновляет своё задание"             test_backup_updates_existing_job
it "backup: совпадающее не трогает"             test_backup_matching_job_is_left_alone
it "backup: не присваивает чужие задания"       test_backup_ignores_foreign_jobs
it "backup: говорит, как убрать чужое задание"  test_backup_warning_tells_how_to_remove
it "backup: сообщает о брошенном своём"         test_backup_reports_orphan_job

printf '\nФаза 2: гости\n'
it "guests: профиль по роли и графике"          test_guests_profile_resolution
it "guests: единицы размера диска"              test_guests_disk_units
it "guests: все профили валидны"                test_guests_all_profiles_are_valid
it "guests: существующего не трогает"           test_guests_existing_is_never_touched
it "guests: сообщает о расхождениях, не правит" test_guests_reports_drift_without_fixing
it "guests: команды создания HAOS"              test_guests_haos_vm_commands
it "guests: нет версии — берём запасную"        test_image_falls_back_when_version_missing
it "guests: называет все адреса, что пробовал"  test_image_names_every_url_it_tried
it "guests: имя файла из ответа GitHub"         test_image_takes_name_from_github_answer
it "guests: версия из манифеста главнее"        test_image_version_from_manifest_wins
it "guests: ставится только выбранный"          test_guest_filter_creates_only_chosen
it "guests: говорит, кого пропустил"            test_guest_filter_says_who_was_skipped
it "guests: машины и контейнеры различаются"    test_guest_kind_splits_vm_and_lxc
it "guests: команды создания ВМ с cloud-init"   test_guests_cloudinit_vm_commands
it "guests: без snippets честно отказывается"   test_guests_cloudinit_needs_snippets_storage
it "guests: команды создания LXC с /dev/dri"    test_guests_lxc_desktop_commands
it "guests: пароль не утекает в лог и на экран" test_guests_lxc_password_never_in_log
it "guests: права на /dev/dri чинятся сами"     test_guests_lxc_fixes_dri_permissions_itself
it "guests: пакеты одной строкой"               test_lxc_install_line_keeps_packages_together
it "guests: просмотр не создаёт файл с паролем" test_dry_run_creates_no_secret_file
it "guests: сценарий удаляет себя в контейнере" test_post_install_script_removes_itself
it "туннель: без токена — отказ"                test_cloudflared_refuses_without_token
it "туннель: токен доезжает, но не светится"    test_cloudflared_installs_service_and_hides_token
it "туннель: просмотр ничего не спрашивает"     test_cloudflared_dry_run_asks_nothing

printf '\nФаза 3: копия конфигурации хоста\n'
it "config-backup: правило нуля"                test_cfgbackup_zero_rule
it "config-backup: собирает архив"              test_cfgbackup_creates_archive
it "config-backup: свежая копия — работы нет"   test_cfgbackup_fresh_copy_is_enough
it "config-backup: устаревшая копия обновляется" test_cfgbackup_stale_copy_triggers_new_one
it "config-backup: прореживает старые"          test_cfgbackup_prunes_old_copies

printf '\nПроброс видеокарты\n'
it "gpu: правило нуля"                          test_gpu_zero_rule
it "gpu: определяет единственную видеокарту"    test_gpu_resolves_single_card
it "gpu: отказ без IOMMU"                       test_gpu_refuses_without_iommu
it "gpu: отказ при общей IOMMU-группе"          test_gpu_refuses_on_shared_group
it "gpu: пишет параметры, модули, привязку"     test_gpu_apply_writes_everything
it "gpu: повторный запуск ничего не делает"     test_gpu_apply_is_idempotent
it "gpu: без подтверждения не трогает хост"     test_gpu_refuses_without_confirmation
it "gpu: записывает, что именно изменил"        test_gpu_state_records_changes
it "gpu: откат возвращает файлы как были"       test_gpu_revert_restores_files
it "gpu: откат без записи — честная ошибка"     test_gpu_revert_without_state_says_so
it "gpu: профиль passthrough настоящий"         test_gpu_passthrough_profile_is_real

printf '\nКомандная строка\n'
it "modules: порядок по числовому префиксу"      test_modules_run_in_numeric_order
it "cli: базовые команды работают"              test_cli_basic_commands_work
it "cli: пример манифеста проходит проверку"    test_cli_validates_example_manifest
it "cli: неизвестная команда — ошибка"          test_cli_rejects_unknown_command
it "cli: говорит, когда пароля нет"             test_cli_password_says_when_there_is_none
it "cli: apply отказывается вне Proxmox"        test_cli_apply_refuses_on_non_proxmox

printf '\nСтатические проверки\n'
it "статика: глобального IFS нет"               test_no_global_ifs
it "статика: интерфейс не рисует окон"          test_ui_draws_no_windows
if ./tests/lint-run-guard.sh >/dev/null 2>&1; then
  printf '  ✓ run-guard: изменения только через run()\n'; PASSED=$(( PASSED + 1 ))
else
  printf '  ✗ run-guard\n'; ./tests/lint-run-guard.sh 2>&1 | sed 's/^/      /'
  FAILED=$(( FAILED + 1 )); FAILED_NAMES+=("run-guard")
fi

if ./tests/lint-no-secrets.sh >/dev/null 2>&1; then
  printf '  ✓ no-secrets: личных данных в репозитории нет\n'; PASSED=$(( PASSED + 1 ))
else
  printf '  ✗ no-secrets\n'; ./tests/lint-no-secrets.sh 2>&1 | sed 's/^/      /'
  FAILED=$(( FAILED + 1 )); FAILED_NAMES+=("no-secrets")
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
