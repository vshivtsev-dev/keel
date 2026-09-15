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

mod_check() {
  config_bool host.updates false || return "$KEEL_RC_SKIP"

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
  local n; n=$(apt_upgradable_count)
  if (( n == 0 )); then
    printf 'все пакеты обновлены\n'
    return "$KEEL_RC_OK"
  fi
  printf 'осталось необновлённых: %s\n' "$n"
  return "$KEEL_RC_CHANGES"
}
