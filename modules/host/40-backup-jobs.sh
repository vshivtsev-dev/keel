#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: расписание резервного копирования
#
# Манифест:
#   "backup": {
#     "schedule": "02:00",        # формат календарных событий Proxmox
#     "storage":  "local",
#     "mode":     "snapshot",     # snapshot | suspend | stop
#     "guests":   [100, 101],     # либо "all": true
#     "keep_last": 3,
#     "compress": "zstd"
#   }
#
# Задание помечается комментарием keel — по нему оно и находится при
# повторных запусках. Чужие задания не трогаются никогда.

readonly KEEL_BACKUP_TAG="keel"

mod_title() { printf 'Резервное копирование гостей'; }

mod_describe() {
  cat <<'EOF'
Заводит задание vzdump по расписанию из манифеста и поддерживает его в
описанном состоянии. Задание помечается комментарием keel; задания,
созданные вручную или другими инструментами, не трогаются.
EOF
}

_backup_configured() { config_has backup.schedule; }

_backup_vmid_list() {
  local n i out=()
  n=$(config_len backup.guests)
  for (( i = 0; i < n; i++ )); do out+=("$(config_get "backup.guests.${i}")"); done
  (( ${#out[@]} )) || return 0
  printf '%s' "$(IFS=,; printf '%s' "${out[*]}")"
}

# Собрать аргументы задания — одно место на check, apply и verify
_backup_args() {
  local vmids; vmids=$(_backup_vmid_list)
  printf '%s\n' "--schedule"     "$(config_get backup.schedule)"
  printf '%s\n' "--storage"      "$(config_get backup.storage local)"
  printf '%s\n' "--mode"         "$(config_get backup.mode snapshot)"
  printf '%s\n' "--compress"     "$(config_get backup.compress zstd)"
  printf '%s\n' "--prune-backups" "keep-last=$(config_get backup.keep_last 3)"
  printf '%s\n' "--comment"      "$KEEL_BACKUP_TAG"
  printf '%s\n' "--enabled"      "1"
  if config_bool backup.all false || [[ -z "$vmids" ]]; then
    printf '%s\n' "--all" "1"
  else
    printf '%s\n' "--vmid" "$vmids"
  fi
}

# Найти наше задание. Печатает id или ничего.
_backup_find_job() {
  local jobs n i comment
  jobs=$(backup_jobs_json)
  n=$(json_len "$jobs" "")
  for (( i = 0; i < n; i++ )); do
    comment=$(json_get "$jobs" "${i}.comment" "")
    if [[ "$comment" == "$KEEL_BACKUP_TAG" ]]; then
      json_get "$jobs" "${i}.id" ""
      return 0
    fi
  done
  return 0
}

# Чем существующее задание отличается от описанного. Пусто — совпадает.
_backup_diff() {
  local jobs n i found=-1 comment
  jobs=$(backup_jobs_json)
  n=$(json_len "$jobs" "")
  for (( i = 0; i < n; i++ )); do
    comment=$(json_get "$jobs" "${i}.comment" "")
    [[ "$comment" == "$KEEL_BACKUP_TAG" ]] && { found=$i; break; }
  done
  (( found < 0 )) && { printf 'задания нет\n'; return 0; }

  local want_schedule want_storage want_mode have
  want_schedule=$(config_get backup.schedule)
  want_storage=$(config_get backup.storage local)
  want_mode=$(config_get backup.mode snapshot)

  have=$(json_get "$jobs" "${found}.schedule" "")
  [[ "$have" == "$want_schedule" ]] || printf 'расписание: %s → %s\n' "${have:-—}" "$want_schedule"
  have=$(json_get "$jobs" "${found}.storage" "")
  [[ "$have" == "$want_storage" ]] || printf 'хранилище: %s → %s\n' "${have:-—}" "$want_storage"
  have=$(json_get "$jobs" "${found}.mode" "")
  [[ "$have" == "$want_mode" ]] || printf 'режим: %s → %s\n' "${have:-—}" "$want_mode"

  local want_vmid; want_vmid=$(_backup_vmid_list)
  if config_bool backup.all false || [[ -z "$want_vmid" ]]; then
    [[ "$(json_get "$jobs" "${found}.all" "0")" == "1" ]] || printf 'охват: → все гости\n'
  else
    have=$(json_get "$jobs" "${found}.vmid" "")
    [[ "$have" == "$want_vmid" ]] || printf 'гости: %s → %s\n' "${have:-—}" "$want_vmid"
  fi
  return 0
}

# Чужое задание «все гости» перекрывает наше: те же гости поедут в копию
# дважды. Трогать его нельзя — чужое, — но и молчать об этом не стоит.
_backup_warn_overlap() {
  local jobs n i comment sched
  jobs=$(backup_jobs_json)
  n=$(json_len "$jobs" "")
  for (( i = 0; i < n; i++ )); do
    comment=$(json_get "$jobs" "${i}.comment" "")
    [[ "$comment" == "$KEEL_BACKUP_TAG" ]] && continue
    [[ "$(json_get "$jobs" "${i}.all" "0")" == "1" ]] || continue
    [[ "$(json_get "$jobs" "${i}.enabled" "1")" == "1" ]] || continue
    sched=$(json_get "$jobs" "${i}.schedule" "?")
    warn "На хосте есть чужое задание «все гости» (${sched}). Гости из манифеста попадут в копию и по нему — дважды за период. keel это задание не трогает."
  done
  return 0
}

mod_check() {
  _backup_configured || return "$KEEL_RC_SKIP"

  # Предупреждение имеет смысл только если наше задание адресное
  if ! config_bool backup.all false && [[ -n "$(_backup_vmid_list)" ]]; then
    _backup_warn_overlap
  fi

  if [[ -z "$(config_get backup.storage local)" ]]; then
    err "backup.storage не может быть пустым"
    return 1
  fi

  local diff; diff=$(_backup_diff)
  if [[ -z "$diff" ]]; then
    return "$KEEL_RC_OK"
  fi
  printf '%s' "$diff"
  return "$KEEL_RC_CHANGES"
}

mod_apply() {
  _backup_configured || return "$KEEL_RC_SKIP"

  local args=() a
  while IFS= read -r a; do args+=("$a"); done < <(_backup_args)

  local id; id=$(_backup_find_job)
  if [[ -n "$id" ]]; then
    run "Обновить задание резервного копирования" \
      pvesh set "/cluster/backup/${id}" "${args[@]}"
  else
    run "Создать задание резервного копирования" \
      pvesh create /cluster/backup "${args[@]}"
  fi
}

mod_verify() {
  _backup_configured || return "$KEEL_RC_SKIP"
  local diff; diff=$(_backup_diff)
  if [[ -z "$diff" ]]; then
    printf 'задание на месте: %s, хранилище %s\n' \
      "$(config_get backup.schedule)" "$(config_get backup.storage local)"
    return "$KEEL_RC_OK"
  fi
  printf '%s' "$diff"
  return "$KEEL_RC_CHANGES"
}
