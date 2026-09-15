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
  done < <(find "${KEEL_ROOT}/modules" -type f -name '*.sh' 2>/dev/null | sort)
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

_module_status_line() {
  local id=$1 rc=$2 title=$3
  case "$rc" in
    "$KEEL_RC_OK")      printf '%s✓%s %-18s %s\n' "$C_GREEN"  "$C_RESET" "$id" "$title" ;;
    "$KEEL_RC_CHANGES") printf '%s→%s %-18s %s\n' "$C_BLUE"   "$C_RESET" "$id" "$title" ;;
    "$KEEL_RC_SKIP")    printf '%s·%s %-18s %s\n' "$C_DIM"    "$C_RESET" "$id" "$title" ;;
    *)                  printf '%s✗%s %-18s %s\n' "$C_RED"    "$C_RESET" "$id" "$title" ;;
  esac
}

# Показать план: что изменится, если применить. Ничего не трогает.
modules_plan() {
  local filter=${1:-}
  local path id title rc out
  local n_changes=0 n_ok=0 n_skip=0 n_err=0

  head1 "План изменений"
  note "Режим просмотра: ничего не выполняется."

  while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    id=$(module_id "$path")
    title=$(module_invoke "$path" title 2>/dev/null || echo "$id")
    rc=0; out=$(module_invoke "$path" check 2>&1) || rc=$?
    _module_status_line "$id" "$rc" "$title"
    case "$rc" in
      "$KEEL_RC_OK")      n_ok=$(( n_ok + 1 )) ;;
      "$KEEL_RC_CHANGES") n_changes=$(( n_changes + 1 )) ;;
      "$KEEL_RC_SKIP")    n_skip=$(( n_skip + 1 )) ;;
      *)                  n_err=$(( n_err + 1 )) ;;
    esac
    if [[ -n "$out" ]]; then
      printf '%s\n' "$out" | sed 's/^/     /'
    fi
  done < <(modules_find "$filter")

  printf '\n'
  info "Изменений: ${n_changes} · уже в порядке: ${n_ok} · не настроено: ${n_skip} · ошибок: ${n_err}"
  if (( n_skip > 0 )); then
    note "«Не настроено» значит: нет соответствующего ключа в манифесте. Это нормально."
  fi
  return 0
}

# Применить. Перед каждым изменением спросит (если режим step).
modules_apply() {
  local filter=${1:-}
  local path id title rc

  head1 "Применение"
  while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    id=$(module_id "$path")
    title=$(module_invoke "$path" title 2>/dev/null || echo "$id")

    rc=0; module_invoke "$path" check >/dev/null 2>&1 || rc=$?
    case "$rc" in
      "$KEEL_RC_OK")   ok "${id}: уже в нужном состоянии"; state_record "$id" apply skipped-ok; continue ;;
      "$KEEL_RC_SKIP") note "${id}: не настроено в манифесте, пропускаю"; state_record "$id" apply unconfigured; continue ;;
      "$KEEL_RC_CHANGES") ;;
      *) err "${id}: проверка завершилась ошибкой (${rc}), модуль пропущен"; state_record "$id" apply check-failed; continue ;;
    esac

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
  done < <(modules_find "$filter")
  return 0
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
      printf '%s\n' "$out" | sed 's/^/     /'
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
