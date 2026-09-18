#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: модули
#
# Модуль — это файл с четырьмя функциями. Больше от него ничего не требуется:
#
#   mod_title     одна строка для меню
#   mod_describe  что модуль делает, человеческим языком
#   mod_check     нужно ли что-то менять; печатает список изменений
#                 коды: KEEL_RC_OK (нечего делать)
#                       KEEL_RC_CHANGES (есть что менять)
#                       KEEL_RC_SKIP (не настроено в манифесте — правило нуля)
#   mod_apply     привести к нужному состоянию, только через run()/run_write()
#   mod_verify    проверить постфактум
#
# Каждый вызов происходит в подоболочке, поэтому модули не мешают друг другу
# и не могут случайно переопределить чужую функцию.

modules_find() {
  local filter=${1:-}
  local f
  while IFS= read -r f; do
    [[ -z "$filter" || "$(module_id "$f")" == "$filter" ]] || continue
    printf '%s\n' "$f"
    # Порядок задаёт числовой префикс имени файла, а не каталог:
    # гости должны создаваться после хранилищ, в каком бы каталоге
    # эти модули ни лежали.
  done < <(find "${KEEL_ROOT}/modules" -type f -name '*.sh' ! -name '_*' \
             -printf '%f\t%p\n' 2>/dev/null | sort | cut -f2-)
}

module_id() {
  local path=$1
  path=${path#"${KEEL_ROOT}/modules/"}
  printf '%s' "${path%.sh}"
}

# module_invoke <файл> <check|apply|verify|title|describe>
module_invoke() {
  local path=$1 verb=$2
  (
    # shellcheck disable=SC1090
    source "$path"
    "mod_${verb}"
  )
}

# Ширина колонки с идентификаторами. Считается по самому длинному из тех, что
# будут показаны, а не берётся константой: константа была 18, а
# host/60-gpu-passthrough — это 23 символа, и на нём колонка разъезжалась,
# сдвигая заголовок вправо ровно у того модуля, который опаснее прочих.
KEEL_PLAN_IDW=12

_modules_id_width() {
  local filter=${1:-} path id w=12
  while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    id=$(module_id "$path")
    # if, а не «&&»: под set -e ложное условие в такой связке роняет keel
    if (( ${#id} > w )); then w=${#id}; fi
  done < <(modules_find "$filter")
  printf '%s' "$w"
}

_module_status_line() {
  local id=$1 rc=$2 title=$3 mark color
  case "$rc" in
    "$KEEL_RC_OK")      mark='✓'; color=$C_GREEN ;;
    "$KEEL_RC_CHANGES") mark='→'; color=$C_BLUE ;;
    "$KEEL_RC_SKIP")    mark='·'; color=$C_DIM ;;
    *)                  mark='✗'; color=$C_RED ;;
  esac
  # pad, а не %-*s: printf считает байты, а не символы, и на русских
  # заголовках выравнивание по ширине поля разъехалось бы снова
  printf '%s%s%s %s %s\n' "$color" "$mark" "$C_RESET" "$(pad "$id" "$KEEL_PLAN_IDW")" "$title"
}

# --- План --------------------------------------------------------------------
#
# Обход всех модулей с mod_check. Печатает план и запоминает результат: пути
# модулей и коды, которые вернули их проверки. Один код на plan и на apply —
# не ради экономии строк, а потому что иначе план и применение однажды
# разойдутся, и человек согласится не с тем, что произойдёт.
#
# Заодно это убирает вторую проверку: раньше apply звал mod_check ещё раз,
# уже молча, и на модуле гостей это означало повторный опрос хоста.
KEEL_PLAN_PATHS=()
KEEL_PLAN_RCS=()
KEEL_PLAN_CHANGES=0

_modules_scan() {
  local filter=${1:-}
  local path id title rc out
  local n_ok=0 n_skip=0 n_err=0

  KEEL_PLAN_PATHS=(); KEEL_PLAN_RCS=(); KEEL_PLAN_CHANGES=0
  KEEL_PLAN_IDW=$(_modules_id_width "$filter")

  while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    id=$(module_id "$path")
    title=$(module_invoke "$path" title 2>/dev/null || echo "$id")
    rc=0; out=$(module_invoke "$path" check 2>&1) || rc=$?
    _module_status_line "$id" "$rc" "$title"
    KEEL_PLAN_PATHS+=("$path")
    KEEL_PLAN_RCS+=("$rc")
    case "$rc" in
      "$KEEL_RC_OK")      n_ok=$(( n_ok + 1 )) ;;
      "$KEEL_RC_CHANGES") KEEL_PLAN_CHANGES=$(( KEEL_PLAN_CHANGES + 1 )) ;;
      "$KEEL_RC_SKIP")    n_skip=$(( n_skip + 1 )) ;;
      *)                  n_err=$(( n_err + 1 )) ;;
    esac
    if [[ -n "$out" ]]; then
      printf '%s\n' "$out" | sed 's/^/    /'
    fi
  done < <(modules_find "$filter")

  printf '\n'
  info "Изменений: ${KEEL_PLAN_CHANGES} · уже в порядке: ${n_ok} · не настроено: ${n_skip} · ошибок: ${n_err}"
  if (( n_skip > 0 )); then
    note "«Не настроено» значит: нет соответствующего ключа в манифесте. Это нормально."
  fi
  return 0
}

# Показать план: что изменится, если применить. Ничего не трогает.
modules_plan() {
  head1 "План изменений"
  note "Режим просмотра: ничего не выполняется."
  _modules_scan "${1:-}"
}

# Тот же план, но развёрнутый до каждой команды и каждого diff. Это ровно то,
# что делает --dry-run: модули прогоняются по-настоящему, а run() вместо
# выполнения печатает. Отдельного кода «показать подробно» поэтому нет —
# был бы второй источник правды о том, что случится.
_modules_detail() {
  local i path
  head1 "Подробный план"
  note "Все команды и правки файлов целиком. Ничего не выполняется."
  for i in "${!KEEL_PLAN_PATHS[@]}"; do
    (( KEEL_PLAN_RCS[i] == KEEL_RC_CHANGES )) || continue
    path=${KEEL_PLAN_PATHS[i]}
    head1 "$(module_id "$path")"
    ( KEEL_MODE="dry"; module_invoke "$path" apply ) || true
  done
}

# Спросить один раз про весь план. Возвращает 0 — применяем, иначе — нет.
# Может поменять KEEL_MODE на step, если человек выбрал пошаговый режим.
_modules_confirm_plan() {
  [[ "$KEEL_MODE" == "once" ]] || return 0
  # Снова не «&&»: при нуле связка вернула бы единицу и set -e уронил бы keel
  if (( KEEL_CONFIRMED )); then return 0; fi

  while true; do
    case "$(ui_confirm_plan "Применить эти изменения? Модулей с изменениями: ${KEEL_PLAN_CHANGES}")" in
      apply)  KEEL_CONFIRMED=1; return 0 ;;
      step)   KEEL_MODE="step"; note "Пошаговый режим: спрошу перед каждым изменением."; return 0 ;;
      detail) _modules_detail ;;
      *)      info "Отменено. Ничего не тронуто."; return 1 ;;
    esac
  done
}

# Собственно применение — уже без вопросов о плане: согласие получено выше.
_modules_apply_run() {
  local i path id title rc started=0

  for i in "${!KEEL_PLAN_PATHS[@]}"; do
    path=${KEEL_PLAN_PATHS[i]}
    id=$(module_id "$path")

    # Модули, которым делать нечего, в журнал попадают, а на экран — нет:
    # их строчки только что проехали в плане, со значком и причиной.
    # Повторять их здесь значит утопить в них те несколько, ради которых
    # человек и нажал «применить».
    case "${KEEL_PLAN_RCS[i]}" in
      "$KEEL_RC_OK")      state_record "$id" apply skipped-ok;   continue ;;
      "$KEEL_RC_SKIP")    state_record "$id" apply unconfigured; continue ;;
      "$KEEL_RC_CHANGES") ;;
      *)                  state_record "$id" apply check-failed; continue ;;
    esac

    if (( ! started )); then head1 "Применение"; started=1; fi
    title=$(module_invoke "$path" title 2>/dev/null || echo "$id")
    head1 "${id} — ${title}"
    rc=0; module_invoke "$path" apply || rc=$?
    if (( rc == KEEL_RC_ABORT )); then
      state_record "$id" apply aborted
      err "Прервано пользователем."
      return "$KEEL_RC_ABORT"
    elif (( rc != 0 )); then
      state_record "$id" apply failed
      err "${id}: применение не удалось (код ${rc})"
      continue
    fi

    if [[ "$KEEL_MODE" == "dry" ]]; then
      note "${id}: показан план, ничего не выполнено"
      continue
    fi

    rc=0; module_invoke "$path" verify >/dev/null 2>&1 || rc=$?
    if (( rc == KEEL_RC_OK )); then
      ok "${id}: применено и проверено"
      state_record "$id" apply ok
    else
      warn "${id}: применено, но проверка не подтвердила результат (код ${rc})"
      state_record "$id" apply applied-unverified
    fi
  done
  return 0
}

# Применить: показать план целиком, спросить один раз, выполнить потоком.
modules_apply() {
  local filter=${1:-}
  local saved_mode=$KEEL_MODE rc=0

  head1 "План изменений"
  _modules_scan "$filter"

  if (( KEEL_PLAN_CHANGES > 0 )) && ! _modules_confirm_plan; then
    KEEL_CONFIRMED=0
    KEEL_MODE=$saved_mode
    return 0
  fi

  # Именно if, а не «&&»: под set -e ложное условие в такой связке роняет keel
  if (( KEEL_PLAN_CHANGES == 0 )); then
    ok "Менять нечего: хост уже соответствует манифесту."
  fi

  # Зовётся и когда менять нечего: применения не будет, но в журнал попадёт,
  # что модули проверены и почему пропущены.
  _modules_apply_run || rc=$?

  # Согласие действует на один прогон. Иначе второй «применить» из меню
  # выполнился бы молча, опираясь на «да», сказанное десять минут назад
  # совсем другому списку. По той же причине возвращается и режим: выбор
  # «по шагам» относился к этому плану, а не ко всей сессии.
  KEEL_CONFIRMED=0
  KEEL_MODE=$saved_mode
  return "$rc"
}

# Проверить текущее состояние, ничего не меняя.
modules_verify() {
  local filter=${1:-}
  local path id title rc out
  head1 "Проверка состояния"
  while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    id=$(module_id "$path")
    title=$(module_invoke "$path" title 2>/dev/null || echo "$id")
    rc=0; out=$(module_invoke "$path" verify 2>&1) || rc=$?
    _module_status_line "$id" "$rc" "$title"
    if [[ -n "$out" ]]; then
      printf '%s\n' "$out" | sed 's/^/    /'
    fi
  done < <(modules_find "$filter")
  return 0
}

modules_list_ids() {
  local path
  while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    printf '%s\t%s\n' "$(module_id "$path")" "$(module_invoke "$path" title 2>/dev/null || echo '-')"
  done < <(modules_find)
}
