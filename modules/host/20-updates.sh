#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: обновление пакетов
#
# Манифест:  "host": { "updates": true }
# Нет ключа — модуль не делает ничего.
#
# Обновление идёт через dist-upgrade, потому что Proxmox иногда меняет
# состав пакетов (ядро, зависимости), и обычный upgrade такие переходы
# пропускает, оставляя систему на полпути.

mod_title() { printf 'Обновление пакетов'; }

mod_describe() {
  cat <<'EOF'
Обновляет список пакетов и ставит доступные обновления (dist-upgrade).
Перед установкой показывает, что именно будет обновлено. Перезагрузку
не делает и не предлагает — это решение остаётся за тобой.
EOF
}

# Битые списки источников apt не печатает как «ошибку обновления»: он просто
# не печатает ничего, и ноль обновлений становится неотличим от «всё свежее».
# Поэтому спрашиваем отдельно и до всего остального.
_updates_apt_blocked() {
  local e; e=$(apt_sources_error)
  [[ -n "$e" ]] || return 1
  err "apt не может прочитать списки источников:"
  printf '%s\n' "$e" | sed 's/^/  /'
  err "Сначала почини репозитории: keel apply --only host/10-repos"
  return 0
}

mod_check() {
  config_bool host.updates false || return "$KEEL_RC_SKIP"
  _updates_apt_blocked && return 1

  local n; n=$(apt_upgradable_count)
  if (( n == 0 )); then
    return "$KEEL_RC_OK"
  fi

  printf 'обновить пакетов: %s\n' "$n"
  apt_upgradable_list | head -n 15 | sed 's/^/  · /'
  if (( n > 15 )); then printf '  · … и ещё %s\n' "$(( n - 15 ))"; fi
  return "$KEEL_RC_CHANGES"
}

mod_apply() {
  config_bool host.updates false || return "$KEEL_RC_SKIP"
  _updates_apt_blocked && return 1

  run "Обновить список пакетов" apt-get update || return $?
  run "Установить обновления" \
    env DEBIAN_FRONTEND=noninteractive apt-get -y dist-upgrade || return $?

  # Смена ядра без перезагрузки — обычное дело, но знать об этом надо
  if [[ -f /var/run/reboot-required ]]; then
    warn "Система просит перезагрузку. keel её не делает — перезагрузи сам, когда удобно."
  fi
  return 0
}

mod_verify() {
  config_bool host.updates false || return "$KEEL_RC_SKIP"
  _updates_apt_blocked && return 1
  local n; n=$(apt_upgradable_count)
  if (( n == 0 )); then
    printf 'все пакеты обновлены\n'
    return "$KEEL_RC_OK"
  fi
  printf 'осталось необновлённых: %s\n' "$n"
  return "$KEEL_RC_CHANGES"
}
