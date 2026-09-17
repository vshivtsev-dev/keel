#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: обновление пакетов
#
# Манифест:
#   "host": {
#     "updates": true,
#     "updates_min_speed": "30K"   # необязательно; "0" — не проверять связь
#   }
# Нет ключа updates — модуль не делает ничего.
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

# Флаги против того, чтобы apt утащил за собой хост. По умолчанию он повторяет
# неудачную закачку трижды и ждёт ответа две минуты: на сети, которая отвечает
# через раз, это максимум ожидания при минимуме результата — и растущая очередь
# закачек, которая и съедает память.
KEEL_APT_OPTS=(-o Acquire::Retries=1 -o Acquire::http::Timeout=30)

# Проба связи перед закачкой. Возвращает 0, если начинать не стоит, — и сама
# объясняет почему. Порога нет в mod_check нарочно: keel plan обязан оставаться
# быстрым и в сеть не лезть.
_updates_net_too_slow() {
  local want; want=$(speed_to_bytes "$(config_get host.updates_min_speed 30K)")
  (( want > 0 )) || return 1            # проверка выключена в манифесте

  local url; url=$(apt_first_upgrade_uri)
  [[ -n "$url" ]] || return 1           # качать нечего — и пробовать нечего

  local got; got=$(net_download_speed "$url")
  if (( got == 0 )); then
    err "Репозиторий не отвечает: ${url}"
    note "Обновление не начинаю: на неотвечающей сети apt копит очередь закачек в памяти, пока хост не кончится."
    note "keel doctor покажет, не уходят ли попытки apt в IPv6, до которого нет маршрута — это самая частая причина."
    return 0
  fi
  if (( got < want )); then
    local total; total=$(apt_upgrade_total_bytes)
    err "Скорость до репозитория $(( got / 1024 )) КБ/с — этого мало (нужно хотя бы $(( want / 1024 )) КБ/с)."
    if (( total > 0 )); then
      local mins=$(( total / got / 60 ))
      (( mins > 0 )) || mins=1
      note "Скачать $(( total / 1024 / 1024 )) МБ на такой скорости — около ${mins} мин, и всё это время apt копит очередь закачек в памяти."
    fi
    note "Порог задаётся ключом host.updates_min_speed, «0» его выключает."
    return 0
  fi
  info "Связь до репозитория: $(( got / 1024 )) КБ/с — начинаю."
  return 1
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

  run "Обновить список пакетов" apt-get update "${KEEL_APT_OPTS[@]}" || return $?

  # Между списком пакетов и закачкой: --print-uris требует свежего списка,
  # а закачка — работающей сети
  _updates_net_too_slow && return 1

  run "Установить обновления" \
    env DEBIAN_FRONTEND=noninteractive apt-get -y dist-upgrade "${KEEL_APT_OPTS[@]}" || return $?

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
