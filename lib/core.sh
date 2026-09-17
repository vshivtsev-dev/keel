#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: ядро
#
# Здесь живут логи, режимы выполнения и — главное — run().
# run() это единственные ворота, через которые keel меняет систему.
# Прямые вызовы изменяющих команд в модулях запрещены; за этим следит
# tests/lint-run-guard.sh в CI. Смысл простой: любое изменение видно
# на экране до того, как случится, и попадает в лог после.

# --- Режимы работы -----------------------------------------------------------
# step — спрашивать перед каждым изменением (по умолчанию)
# yes  — не спрашивать (используется после просмотра плана)
# dry  — ничего не выполнять, только показывать
KEEL_MODE="${KEEL_MODE:-step}"

# --- Один каталог на всё ------------------------------------------------------
#
# Всё, с чем работает человек, лежит в KEEL_HOME и больше нигде: манифест,
# пароли, копии файлов, логи, журнал. Код keel живёт в подкаталоге app/ —
# так обновление никогда не задевает данные, а данные не мешают читать код.
# Каждый путь переопределяется окружением: этим пользуются тесты.
KEEL_HOME="${KEEL_HOME:-/root/keel}"

KEEL_LOG_DIR="${KEEL_LOG_DIR:-${KEEL_HOME}/logs}"
KEEL_STATE_DIR="${KEEL_STATE_DIR:-${KEEL_HOME}}"
KEEL_BACKUP_DIR="${KEEL_BACKUP_DIR:-${KEEL_STATE_DIR}/backups}"
KEEL_LOG_FILE="${KEEL_LOG_FILE:-}"

# Секреты — пароли гостей и токены — отдельным каталогом с правами 0600
KEEL_SECRETS_DIR="${KEEL_SECRETS_DIR:-${KEEL_STATE_DIR}/secrets}"

# --- Секреты, которых не должно быть на экране --------------------------------
#
# Пароли и токены попадают в сценарии, которые keel показывает перед
# выполнением. Показывать их нельзя. Замена идёт средствами bash, а не sed:
# у sed пришлось бы экранировать слэши и метасимволы, а ошибка в экранировании
# здесь означает утёкший секрет.
KEEL_MASK=()

keel_mask_add() {
  [[ -n "${1:-}" ]] || return 0
  KEEL_MASK+=("$1")
  return 0
}

keel_mask_apply() {
  local text=$1 secret
  for secret in ${KEEL_MASK[@]+"${KEEL_MASK[@]}"}; do
    [[ -n "$secret" ]] || continue
    text=${text//"$secret"/********}
  done
  printf '%s' "$text"
}

# --- Коды возврата модулей ---------------------------------------------------
# Договорённость на весь проект: 0 всегда значит "всё хорошо, делать нечего".
# shellcheck disable=SC2034  # читаются модулями и lib/modules.sh
readonly KEEL_RC_OK=0          # состояние уже такое, как надо
# shellcheck disable=SC2034
readonly KEEL_RC_CHANGES=10    # есть что изменить
# shellcheck disable=SC2034
readonly KEEL_RC_SKIP=20       # не настроено в манифесте — правило нуля
readonly KEEL_RC_ABORT=3       # пользователь прервал выполнение

# --- Цвета (только если это настоящий терминал) ------------------------------
if [[ -t 1 ]]; then
  C_RESET=$'\033[0m'; C_BOLD=$'\033[1m'; C_DIM=$'\033[2m'
  C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_BLUE=$'\033[34m'
else
  C_RESET=''; C_BOLD=''; C_DIM=''
  C_RED=''; C_GREEN=''; C_YELLOW=''; C_BLUE=''
fi

# --- Логи --------------------------------------------------------------------

# printf '%-20s' выравнивает по байтам, а русские подписи многобайтовые —
# из-за этого колонки разъезжаются. Считаем символы и дополняем вручную.
pad() {
  local s=$1 width=$2
  # Отдельной строкой: bash раскрывает все слова команды до присваивания,
  # поэтому ${#s} в одном local с s=$1 ещё не видит значения.
  local len=${#s}
  printf '%s' "$s"
  if (( len < width )); then printf '%*s' $(( width - len )) ''; fi
}

# Чтобы ${#s} считал символы, а не байты, нужна UTF-8 локаль.
# Ставим её только для самого keel и только если своей нет.
_core_locale() {
  case "${LC_ALL:-${LC_CTYPE:-${LANG:-}}}" in
    *UTF-8*|*utf8*) return 0 ;;
  esac
  command -v locale >/dev/null 2>&1 || return 0
  # В разных системах она зовётся то C.UTF-8, то C.utf8 — сравниваем без дефисов
  if locale -a 2>/dev/null | tr 'A-Z' 'a-z' | tr -d '-' | grep -qx 'c.utf8'; then
    export LC_ALL=C.UTF-8
    return 0
  fi
  local any
  any=$(locale -a 2>/dev/null | grep -i -m1 'utf-\?8') || return 0
  [[ -n "$any" ]] && export LC_ALL="$any"
  return 0
}

core_init() {
  _core_locale
  if [[ -n "$KEEL_LOG_FILE" ]]; then
    return 0
  fi
  if mkdir -p "$KEEL_LOG_DIR" 2>/dev/null && [[ -w "$KEEL_LOG_DIR" ]]; then
    KEEL_LOG_FILE="${KEEL_LOG_DIR}/$(date +%Y-%m-%d_%H%M%S).log"
  else
    # Нет прав на /var/log — не повод падать: пишем во временный каталог
    KEEL_LOG_DIR="${TMPDIR:-/tmp}/keel-logs"
    mkdir -p "$KEEL_LOG_DIR"
    KEEL_LOG_FILE="${KEEL_LOG_DIR}/$(date +%Y-%m-%d_%H%M%S).log"
  fi
  : >"$KEEL_LOG_FILE"
  keel_log "keel запущен: режим=${KEEL_MODE} пользователь=$(id -un) хост=$(hostname 2>/dev/null || echo '?')"
}

keel_log() {
  [[ -n "$KEEL_LOG_FILE" ]] || return 0
  printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >>"$KEEL_LOG_FILE"
}

info()  { printf '%s\n' "$*"; keel_log "INFO: $*"; }
ok()    { printf '%s✓%s %s\n' "$C_GREEN" "$C_RESET" "$*"; keel_log "OK: $*"; }
warn()  { printf '%s!%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; keel_log "WARN: $*"; }
err()   { printf '%s✗%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; keel_log "ERROR: $*"; }
note()  { printf '%s  %s%s\n' "$C_DIM" "$*" "$C_RESET"; keel_log "NOTE: $*"; }
head1() { printf '\n%s%s%s\n' "$C_BOLD" "$*" "$C_RESET"; keel_log "== $* =="; }

die() {
  err "$*"
  exit 1
}

# Показать команду так, как её можно скопировать в терминал
cmd_str() {
  local out='' arg
  for arg in "$@"; do
    if [[ "$arg" =~ ^[A-Za-z0-9_@%^+=:,./-]+$ ]]; then
      out+="${arg} "
    else
      out+="'${arg//\'/\'\\\'\'}' "
    fi
  done
  printf '%s' "${out% }"
}

# --- Единственные ворота для изменений системы -------------------------------

# run "человеческое описание" команда аргументы...
#
# step: показать команду, спросить (применить / пропустить / прервать)
# yes:  выполнить, залогировать
# dry:  только показать
run() {
  local desc=$1; shift
  local rendered
  rendered=$(cmd_str "$@")

  keel_log "STEP: ${desc}"
  keel_log "CMD:  ${rendered}"

  case "$KEEL_MODE" in
    dry)
      printf '%s→%s %s\n' "$C_BLUE" "$C_RESET" "$desc"
      printf '%s    %s%s\n' "$C_DIM" "$rendered" "$C_RESET"
      return 0
      ;;
    step)
      local answer
      answer=$(ui_confirm_step "$desc" "$rendered")
      case "$answer" in
        apply) ;;
        skip)
          warn "Пропущено: ${desc}"
          return 0
          ;;
        *)
          keel_log "ABORT: прервано пользователем на шаге: ${desc}"
          err "Прервано."
          exit "$KEEL_RC_ABORT"
          ;;
      esac
      ;;
    yes) ;;
    *) die "Неизвестный режим KEEL_MODE=${KEEL_MODE}" ;;
  esac

  printf '%s→%s %s\n' "$C_BLUE" "$C_RESET" "$desc"
  # Вывод идёт и на экран, и в лог. Скачивание образа на 500 МБ или
  # dist-upgrade длятся минутами: молчащий экран выглядит как зависание.
  local rc=0
  "$@" 2>&1 | tee -a "$KEEL_LOG_FILE"
  rc=${PIPESTATUS[0]}
  if (( rc != 0 )); then
    err "Команда завершилась с кодом ${rc}: ${rendered}"
    note "Подробности в логе: ${KEEL_LOG_FILE}"
    return "$rc"
  fi
  keel_log "DONE: ${desc}"
  return 0
}

# Копия файла в /root/keel/backups перед любой правкой.
# Делается всегда и молча — это не изменение состояния, а страховка.
keel_backup_file() {
  local path=$1
  [[ -e "$path" ]] || return 0
  local stamp dest
  stamp="${KEEL_RUN_STAMP:-$(date +%Y-%m-%d_%H%M%S)}"
  dest="${KEEL_BACKUP_DIR}/${stamp}${path}"
  mkdir -p "$(dirname "$dest")" 2>/dev/null || return 0
  cp -a "$path" "$dest" 2>/dev/null || return 0
  keel_log "BACKUP: ${path} -> ${dest}"
}

# run_write "описание" /путь/к/файлу   < новое-содержимое
#
# Показывает diff, делает резервную копию, пишет файл — всё через те же ворота.
run_write() {
  local desc=$1 path=$2
  local tmp diff_out
  tmp=$(mktemp)
  cat >"$tmp"
  # Конфиги без перевода строки в конце — источник тихих сюрпризов
  [[ -s "$tmp" && -z "$(tail -c1 "$tmp")" ]] || printf '\n' >>"$tmp"

  if [[ -f "$path" ]] && diff -q "$path" "$tmp" >/dev/null 2>&1; then
    rm -f "$tmp"
    keel_log "NOCHANGE: ${path} уже в нужном состоянии"
    return 0
  fi

  diff_out=$(diff -u --label "${path} (сейчас)" --label "${path} (станет)" \
    <(cat "$path" 2>/dev/null) "$tmp" 2>/dev/null || true)
  keel_log "DIFF для ${path}:"
  keel_log "${diff_out}"

  case "$KEEL_MODE" in
    dry)
      printf '%s→%s %s\n' "$C_BLUE" "$C_RESET" "$desc"
      printf '%s\n' "$diff_out"
      rm -f "$tmp"
      return 0
      ;;
    step)
      local answer
      answer=$(ui_confirm_step "$desc" "$diff_out")
      case "$answer" in
        apply) ;;
        skip) warn "Пропущено: ${desc}"; rm -f "$tmp"; return 0 ;;
        *) rm -f "$tmp"; err "Прервано."; exit "$KEEL_RC_ABORT" ;;
      esac
      ;;
    yes) ;;
  esac

  keel_backup_file "$path"
  printf '%s→%s %s\n' "$C_BLUE" "$C_RESET" "$desc"
  local rc=0
  mkdir -p "$(dirname "$path")" || rc=$?
  cat "$tmp" >"$path" || rc=$?
  rm -f "$tmp"
  if (( rc != 0 )); then
    err "Не удалось записать ${path}"
    return "$rc"
  fi
  keel_log "WROTE: ${path}"
  return 0
}

# Все системные пути модули строят через fsroot(). В обычной работе это
# пустая обёртка, в тестах KEEL_FS_ROOT указывает на временный каталог —
# так проверки гоняются по-настоящему, но мимо живой системы.
KEEL_FS_ROOT="${KEEL_FS_ROOT:-}"

fsroot() { printf '%s%s' "$KEEL_FS_ROOT" "$1"; }

# Удаление временного каталога. Живёт в ядре, а не в модуле: там любой
# rm справедливо считается изменением системы и требует run().
keel_tmp_cleanup() {
  local dir=$1
  [[ -n "$dir" && "$dir" == /tmp/* || "$dir" == "${TMPDIR:-/tmp}"/* ]] || return 0
  rm -rf "$dir"
}

need_root() {
  [[ "$(id -u)" -eq 0 ]] || die "Нужны права root. Запусти под root или через sudo."
}
