#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: резервная копия конфигурации хоста
#
# Манифест:
#   "host": {
#     "config_backup": {
#       "path": "/var/lib/vz/dump/keel-config",
#       "keep": 7,
#       "max_age_hours": 24
#     }
#   }
#
# Бэкапы виртуалок делает vzdump. А вот сам хост — его сеть, хранилища,
# список гостей, ключи — не бэкапит никто. Именно этого и не хватает,
# когда всё сгорело: гости-то восстановятся, а куда их класть и как
# называется мост — уже нет.
#
# Копия — обычный tar.gz, который читается чем угодно. Держи его не на
# этом же хосте: копия рядом с оригиналом спасает только от опечаток.

mod_title() { printf 'Резервная копия конфигурации хоста'; }

mod_describe() {
  cat <<'EOF'
Складывает в архив /etc/pve, настройки сети, хранилищ, apt, загрузчика и
текстовый снимок состояния (версии, диски, список гостей). Старые архивы
прореживает, оставляя указанное количество. Ничего в системе не меняет,
кроме собственного каталога с архивами.
EOF
}

_cb_dir()   { config_get host.config_backup.path ""; }
_cb_keep()  { config_get host.config_backup.keep 7; }
_cb_maxage() { config_get host.config_backup.max_age_hours 24; }

_cb_latest() {
  local dir; dir=$(_cb_dir)
  [[ -d "$dir" ]] || return 0
  find "$dir" -maxdepth 1 -name 'keel-host-*.tar.gz' -printf '%T@ %p\n' 2>/dev/null \
    | sort -rn | head -n1 | cut -d' ' -f2-
}

_cb_age_hours() {
  local file=$1 mtime now
  [[ -f "$file" ]] || { printf '999999'; return 0; }
  mtime=$(stat -c %Y "$file")
  now=$(date +%s)
  printf '%s' $(( (now - mtime) / 3600 ))
}

mod_check() {
  local dir; dir=$(_cb_dir)
  [[ -n "$dir" ]] || return "$KEEL_RC_SKIP"

  local latest age
  latest=$(_cb_latest)
  if [[ -z "$latest" ]]; then
    printf 'сделать первую копию конфигурации в %s\n' "$dir"
    return "$KEEL_RC_CHANGES"
  fi

  age=$(_cb_age_hours "$latest")
  if (( age < $(_cb_maxage) )); then
    return "$KEEL_RC_OK"
  fi
  printf 'последней копии %s ч (порог %s ч) — сделать свежую\n' "$age" "$(_cb_maxage)"
  return "$KEEL_RC_CHANGES"
}

mod_apply() {
  local dir; dir=$(_cb_dir)
  [[ -n "$dir" ]] || return "$KEEL_RC_SKIP"

  local stamp archive
  stamp=$(date +%Y-%m-%d_%H%M%S)
  archive="${dir}/keel-host-${stamp}.tar.gz"

  [[ -d "$dir" ]] || run "Создать каталог для копий ${dir}" mkdir -p "$dir" || return $?

  local paths=() p
  while IFS= read -r p; do [[ -n "$p" ]] && paths+=("$p"); done < <(host_config_paths)
  if (( ${#paths[@]} == 0 )); then
    err "Не нашлось ни одного файла конфигурации — это точно хост Proxmox?"
    return 1
  fi

  # Снимок состояния и манифест кладутся рядом с файлами: без них архив —
  # просто набор конфигов без объяснения, от какой он машины
  local staging rc=0
  staging=$(host_config_staging_dir)

  run "Собрать архив конфигурации ${archive##*/}" \
    tar -czf "$archive" --ignore-failed-read \
      -C "$staging" . \
      -C "$(fsroot /)" "${paths[@]}" || rc=$?

  keel_tmp_cleanup "$staging"
  (( rc == 0 )) || return "$rc"

  # Прореживание: оставляем только свежие копии
  local keep; keep=$(_cb_keep)
  local old
  while IFS= read -r old; do
    [[ -n "$old" ]] || continue
    run "Удалить устаревшую копию ${old##*/}" rm -f "$old" || true
  done < <(find "$dir" -maxdepth 1 -name 'keel-host-*.tar.gz' -printf '%T@ %p\n' 2>/dev/null \
            | sort -rn | tail -n +$(( keep + 1 )) | cut -d' ' -f2-)

  if [[ -f "$archive" ]]; then
    ok "Копия конфигурации: ${archive}"
    warn "Держи её не только на этом хосте — копия рядом с оригиналом спасает лишь от опечаток."
  fi
  return 0
}

mod_verify() {
  local dir; dir=$(_cb_dir)
  [[ -n "$dir" ]] || return "$KEEL_RC_SKIP"

  local latest; latest=$(_cb_latest)
  if [[ -z "$latest" ]]; then
    printf 'копий конфигурации нет\n'
    return "$KEEL_RC_CHANGES"
  fi
  local age; age=$(_cb_age_hours "$latest")
  printf 'последняя копия: %s (%s ч назад)\n' "${latest##*/}" "$age"
  if (( age >= $(_cb_maxage) )); then
    return "$KEEL_RC_CHANGES"
  fi
  return "$KEEL_RC_OK"
}
