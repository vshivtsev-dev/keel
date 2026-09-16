#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: хранилища
#
# Манифест:
#   "storages": [
#     { "name": "local", "content": ["iso", "vztmpl", "backup", "snippets"] },
#     { "name": "media", "type": "dir", "path": "/mnt/media", "content": ["iso"] }
#   ]
#
# Что делает:
#   · у существующего хранилища ДОБАВЛЯЕТ описанные типы content к тем,
#     что уже есть — не затирая набор целиком
#   · создаёт отсутствующее хранилище, но только типа dir — создавать
#     LVM или ZFS автоматически слишком опасно, это отдельное решение
#
# Чего не делает никогда: не удаляет хранилища и не трогает те, которых
# нет в манифесте.

mod_title() { printf 'Хранилища'; }

mod_describe() {
  cat <<'EOF'
Добавляет хранилищам типы содержимого (content), описанные в манифесте, и
создаёт отсутствующие хранилища типа dir. Типы, которых нет в манифесте, но
есть на хосте, остаются на месте: ничего не удаляется. Хранилища, которых нет
в манифесте, не трогает.
EOF
}

_storages_count() { config_len storages; }

# Нормализованный список content: через запятую, по алфавиту
_storages_want_content() {
  local idx=$1 n i out=()
  n=$(config_len "storages.${idx}.content")
  for (( i = 0; i < n; i++ )); do
    out+=("$(config_get "storages.${idx}.content.${i}")")
  done
  (( ${#out[@]} )) || return 0
  printf '%s' "$(printf '%s\n' "${out[@]}" | sort | paste -sd, -)"
}

# Типы из want, которых ещё нет у хранилища. Пусто — добавлять нечего.
_storages_missing_content() {
  local want=$1 have=$2 t out=()
  while IFS= read -r t; do
    [[ -n "$t" ]] || continue
    [[ ",${have}," == *",${t},"* ]] || out+=("$t")
  done < <(printf '%s\n' "${want//,/$'\n'}")
  (( ${#out[@]} )) || return 0
  printf '%s' "$(IFS=,; printf '%s' "${out[*]}")"
}

# Что должно получиться: то, что было, плюс то, что описано
_storages_union_content() {
  local want=$1 have=$2
  printf '%s\n%s\n' "${want//,/$'\n'}" "${have//,/$'\n'}" \
    | sed '/^[[:space:]]*$/d' | sort -u | paste -sd, -
}

_storages_have_content() {
  local name=$1 cur
  cur=$(storage_content "$name" 2>/dev/null) || return 0
  [[ -n "$cur" ]] || return 0
  printf '%s' "$(printf '%s\n' "${cur//,/$'\n'}" | sort | paste -sd, -)"
}

mod_check() {
  local n; n=$(_storages_count)
  (( n > 0 )) || return "$KEEL_RC_SKIP"

  local changes=0 i name type path want have
  for (( i = 0; i < n; i++ )); do
    name=$(config_get "storages.${i}.name")
    [[ -n "$name" ]] || { err "storages[${i}]: не указано name"; return 1; }
    want=$(_storages_want_content "$i")

    if ! storage_exists "$name"; then
      type=$(config_get "storages.${i}.type" "")
      path=$(config_get "storages.${i}.path" "")
      if [[ "$type" != "dir" || -z "$path" ]]; then
        printf 'хранилища «%s» нет; создать можно только type=dir с path — опиши их или заведи хранилище сам\n' "$name"
        changes=1
        continue
      fi
      printf 'создать хранилище %s (dir, %s)%s\n' "$name" "$path" \
        "${want:+, content=${want}}"
      changes=1
      continue
    fi

    have=$(_storages_have_content "$name")
    local missing; missing=$(_storages_missing_content "$want" "$have")
    if [[ -n "$want" && -n "$missing" ]]; then
      printf 'у хранилища %s добавить content: %s (было: %s)\n' \
        "$name" "$missing" "${have:-—}"
      changes=1
    fi
  done

  (( changes )) && return "$KEEL_RC_CHANGES"
  return "$KEEL_RC_OK"
}

mod_apply() {
  local n; n=$(_storages_count)
  (( n > 0 )) || return "$KEEL_RC_SKIP"

  local i name type path want have
  for (( i = 0; i < n; i++ )); do
    name=$(config_get "storages.${i}.name")
    want=$(_storages_want_content "$i")

    if ! storage_exists "$name"; then
      type=$(config_get "storages.${i}.type" "")
      path=$(config_get "storages.${i}.path" "")
      if [[ "$type" != "dir" || -z "$path" ]]; then
        warn "Хранилище «${name}» не создаю: автоматически заводится только type=dir с path."
        continue
      fi
      if [[ ! -d "$path" ]]; then
        run "Создать каталог ${path}" mkdir -p "$path" || return $?
      fi
      local add_args=(pvesm add dir "$name" --path "$path")
      [[ -n "$want" ]] && add_args+=(--content "$want")
      run "Добавить хранилище ${name}" "${add_args[@]}" || return $?
      continue
    fi

    have=$(_storages_have_content "$name")
    local missing merged
    missing=$(_storages_missing_content "$want" "$have")
    if [[ -n "$want" && -n "$missing" ]]; then
      merged=$(_storages_union_content "$want" "$have")
      run "Добавить content у ${name}: ${missing} (станет ${merged})" \
        pvesm set "$name" --content "$merged" || return $?
    fi
  done
  return 0
}

mod_verify() {
  local n; n=$(_storages_count)
  (( n > 0 )) || return "$KEEL_RC_SKIP"

  local rc=0 i name want have
  for (( i = 0; i < n; i++ )); do
    name=$(config_get "storages.${i}.name")
    want=$(_storages_want_content "$i")
    if ! storage_exists "$name"; then
      printf 'нет хранилища: %s\n' "$name"
      rc=$KEEL_RC_CHANGES
      continue
    fi
    have=$(_storages_have_content "$name")
    local missing; missing=$(_storages_missing_content "$want" "$have")
    if [[ -n "$want" && -n "$missing" ]]; then
      printf '%s: нет типов content: %s (есть: %s)\n' "$name" "$missing" "${have:-—}"
      rc=$KEEL_RC_CHANGES
    else
      printf '%s: в порядке\n' "$name"
    fi
  done
  return "$rc"
}
