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
# once — показать весь план, спросить один раз, дальше выполнять потоком
#        (по умолчанию)
# step — спрашивать перед каждым изменением (--step)
# yes  — не спрашивать вообще (--yes)
# dry  — ничего не выполнять, только показывать (--dry-run)
#
# Почему по умолчанию не step. Вопрос на каждую команду выглядел разумно, пока
# команд было пять. На примерном манифесте с тремя гостями их тридцать
# (посчитано: keel apply --dry-run | grep -c '^→'), и каждый ответ — это окно,
# которое затирает экран: что уже сделано, к моменту десятого вопроса не видно.
# Поэтому теперь весь список показывается заранее, согласие берётся один раз, а
# дальше работа идёт сплошным потоком, как у apt и terraform. Пошаговый режим
# никуда не делся — он под флагом --step.
KEEL_MODE="${KEEL_MODE:-once}"

# Согласие на план целиком уже получено. Ставит modules_apply, читает run():
# так обещание «спросим один раз» держится даже для команд, которые модуль
# собирает на лету. Ноль значит «согласия не было» — и тогда режим once
# спрашивает о каждом шаге, как step. Это не перестраховка: run() вызывают и
# в обход плана — откат видеокарты, внешние сценарии, — и молча выполнять
# там нельзя.
KEEL_CONFIRMED="${KEEL_CONFIRMED:-0}"

# Есть ли терминал, у которого вообще можно что-то спросить.
#
# Проверяется открытием, а не через [[ -e /dev/tty ]]: файл устройства есть и
# в контейнере, и в systemd-юните, и под nohup, но открыть его там нельзя —
# «No such device or address». Разница не косметическая: read из неоткрытого
# /dev/tty оставляет ответ пустым, а пустой ответ раньше означал «применить».
keel_have_tty() { { : </dev/tty; } 2>/dev/null; }

# Название режима для человека: в шапке меню «once» ничего не объясняет
keel_mode_title() {
  case "$KEEL_MODE" in
    once) printf 'спрошу один раз' ;;
    step) printf 'спрошу на каждом шаге' ;;
    yes)  printf 'без вопросов' ;;
    dry)  printf 'только показ, ничего не выполняю' ;;
    *)    printf '%s' "$KEEL_MODE" ;;
  esac
}

# Можно ли сейчас задать человеку вопрос. Не то же самое, что «есть терминал»:
# при --yes терминал есть, но спрашивать запрещено.
keel_interactive() {
  case "$KEEL_MODE" in
    once|step) keel_have_tty ;;
    *)         return 1 ;;
  esac
}

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

# --- Цвета -------------------------------------------------------------------
#
# Пустые значения — состояние по умолчанию, а не запасное: die() может
# сработать на разборе флагов, то есть до core_init, и под set -u неназначенная
# переменная уронила бы keel на попытке сообщить об ошибке.
#
# Включаются они в core_init, потому что решение зависит от флага --plain,
# а флаги разбираются раньше. Коды здесь только для цвета символов (31–34):
# фон не задаётся нигде и никогда — keel красит слово, а не терминал.
C_RESET=''; C_BOLD=''; C_DIM=''
C_RED=''; C_GREEN=''; C_YELLOW=''; C_BLUE=''

KEEL_COLOR="${KEEL_COLOR:-auto}"   # auto | off

_core_colors() {
  [[ "$KEEL_COLOR" != "off" ]] || return 0
  [[ -t 1 ]] || return 0
  C_RESET=$'\033[0m'; C_BOLD=$'\033[1m'; C_DIM=$'\033[2m'
  C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_BLUE=$'\033[34m'
}

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
  _core_colors
  case "$KEEL_MODE" in
    once|step|yes|dry) ;;
    *) die "Неизвестный режим KEEL_MODE=${KEEL_MODE} (once, step, yes, dry)" ;;
  esac
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

# Будет ли о конкретном шаге задан отдельный вопрос.
#
# Один источник правды на все три места, где это важно: сами ворота, печать
# команды в run() и печать diff в run_write(). Разъехавшись, они дали бы не
# падение, а тихую неправильность — лишний вопрос или молча выполненную
# команду, — поэтому условие живёт здесь, а не переписывается по месту.
#
# Неизвестный режим считается спрашивающим: ошибиться в сторону вопроса
# безопаснее, чем в сторону молчания. Само значение проверяется в core_init.
_run_will_ask() {
  case "$KEEL_MODE" in
    yes|dry) return 1 ;;
    once)    (( ! KEEL_CONFIRMED )) ;;
    *)       return 0 ;;
  esac
}

# Спросить про один шаг — или не спрашивать, если режим уже всё решил.
# Печатает ровно одно слово: apply | skip | abort.
#
# Падать здесь нельзя: функция живёт в подстановке $(...), то есть в
# подоболочке, и die() оборвал бы её, а не keel — вызывающая сторона получила
# бы пустой ответ и поняла его как «прервать».
_run_gate() {
  local desc=$1 body=$2
  if ! _run_will_ask; then
    printf 'apply'
    return 0
  fi
  ui_confirm_step "$desc" "$body"
}

# run "человеческое описание" команда аргументы...
#
# dry:  только показать
# once: выполнить, если план уже подтверждён; иначе спросить, как step
# step: показать команду, спросить (применить / пропустить / прервать)
# yes:  выполнить, залогировать
run() {
  local desc=$1; shift
  local rendered
  rendered=$(cmd_str "$@")

  keel_log "STEP: ${desc}"
  keel_log "CMD:  ${rendered}"

  if [[ "$KEEL_MODE" == "dry" ]]; then
    printf '%s→%s %s\n' "$C_BLUE" "$C_RESET" "$desc"
    printf '%s    %s%s\n' "$C_DIM" "$rendered" "$C_RESET"
    return 0
  fi

  # Спрашивали ли про этот шаг отдельно — от этого зависит, показывать ли
  # команду ещё раз ниже. Считается до вопроса: ответ на него KEEL_CONFIRMED
  # не меняет, но читать состояние после чужого вызова — верный способ
  # однажды получить не то, что было на входе.
  local asked=0
  if _run_will_ask; then asked=1; fi

  case "$(_run_gate "$desc" "$rendered")" in
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

  printf '%s→%s %s\n' "$C_BLUE" "$C_RESET" "$desc"
  # Команда печатается и при обычном применении, а не только в dry: без этой
  # строки поток был бы списком намерений без единого доказательства, что
  # именно выполнялось. Кроме случая, когда о шаге только что спросили, —
  # там команда уже на экране, и повторять её незачем.
  if (( ! asked )); then
    printf '%s    %s%s\n' "$C_DIM" "$rendered" "$C_RESET"
  fi
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

  if [[ "$KEEL_MODE" == "dry" ]]; then
    printf '%s→%s %s\n' "$C_BLUE" "$C_RESET" "$desc"
    printf '%s\n' "$diff_out"
    rm -f "$tmp"
    return 0
  fi

  # Спрашивали ли про этот шаг отдельно — от этого зависит, показывать ли
  # diff ещё раз ниже
  local asked=0
  if _run_will_ask; then asked=1; fi

  case "$(_run_gate "$desc" "$diff_out")" in
    apply) ;;
    skip) warn "Пропущено: ${desc}"; rm -f "$tmp"; return 0 ;;
    *) rm -f "$tmp"; err "Прервано."; exit "$KEEL_RC_ABORT" ;;
  esac

  keel_backup_file "$path"
  printf '%s→%s %s\n' "$C_BLUE" "$C_RESET" "$desc"
  # Правка файла — единственное изменение, которого не видно по выводу самой
  # команды: cat в файл ничего не печатает. Поэтому diff идёт в поток, целиком,
  # и остаётся в прокрутке ровно там, где случилась правка. И ровно один раз:
  # в пошаговом режиме его только что напечатал сам вопрос.
  if (( ! asked )); then
    local diff_line
    while IFS= read -r diff_line; do
      printf '%s    %s%s\n' "$C_DIM" "$diff_line" "$C_RESET"
    done <<<"$diff_out"
  fi
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
