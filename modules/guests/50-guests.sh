#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: гости из манифеста
#
# Манифест:
#   "guests": [
#     { "id": 100, "name": "haos", "profile": "haos", "memory": 4096 },
#     { "id": 101, "name": "desktop", "profile": "desktop", "graphics": "dri" }
#   ]
#
# Существующий гость не трогается никогда. Совпал id — keel сообщит о
# расхождениях с манифестом и пройдёт мимо. Виртуалка с данными дороже
# любой стройности; приводить её к описанию — твоё решение, не скрипта.

mod_title() { printf 'Гости: виртуальные машины и контейнеры'; }

mod_describe() {
  cat <<'EOF'
Создаёт описанных в манифесте гостей: виртуальные машины из образов и
облачных образов, контейнеры LXC. Существующих гостей не изменяет и не
удаляет — только сообщает, чем они отличаются от описания.
EOF
}

mod_check() {
  local n; n=$(guests_count)
  (( n > 0 )) || return "$KEEL_RC_SKIP"

  local changes=0 i id name
  for (( i = 0; i < n; i++ )); do
    id=$(config_get "guests.${i}.id")
    name=$(config_get "guests.${i}.name" "без имени")
    guest_prepare "$i" || return 1

    if guest_exists "$id"; then
      printf '%s «%s» уже есть — не трогаю\n' "$id" "$name"
      guest_report_drift "$i" "$id"
      continue
    fi
    guest_plan "$i" || return 1
    changes=1
  done

  (( changes )) && return "$KEEL_RC_CHANGES"
  return "$KEEL_RC_OK"
}

mod_apply() {
  local n; n=$(guests_count)
  (( n > 0 )) || return "$KEEL_RC_SKIP"

  local i id name
  for (( i = 0; i < n; i++ )); do
    id=$(config_get "guests.${i}.id")
    name=$(config_get "guests.${i}.name" "без имени")
    guest_prepare "$i" || return 1

    if guest_exists "$id"; then
      note "${id} «${name}» уже существует — пропускаю"
      continue
    fi
    head1 "Гость ${id}: ${name}"
    guest_apply "$i" || return $?
  done
  return 0
}

mod_verify() {
  local n; n=$(guests_count)
  (( n > 0 )) || return "$KEEL_RC_SKIP"

  local rc=0 i id name
  for (( i = 0; i < n; i++ )); do
    id=$(config_get "guests.${i}.id")
    name=$(config_get "guests.${i}.name" "без имени")
    if guest_exists "$id"; then
      printf '%s «%s»: на месте\n' "$id" "$name"
      guest_prepare "$i" >/dev/null 2>&1 && guest_report_drift "$i" "$id"
    else
      printf '%s «%s»: нет на хосте\n' "$id" "$name"
      rc=$KEEL_RC_CHANGES
    fi
  done
  return "$rc"
}
