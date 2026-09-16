#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: проброс видеокарты в виртуальную машину (вариант C)
#
# Манифест:
#   "host": {
#     "gpu_passthrough": {
#       "enabled": true,
#       "device": "auto",     # или PCI-адрес, например "64:00.0"
#       "vm": 201             # кому отдать; только гость из твоего манифеста
#     }
#   }
#
# Нет ключа или enabled=false — модуль не делает ничего.
#
# Это самый опасный модуль в keel. Если видеокарта на хосте одна, после
# перезагрузки локальный монитор погаснет навсегда. Поэтому: подтверждение
# с набором адреса устройства, запись обо всех изменениях и команда отката
# `keel gpu revert`, которая возвращает файлы из резервных копий.

mod_title() { printf 'Проброс видеокарты в ВМ'; }

mod_describe() {
  cat <<'EOF'
Отдаёт видеокарту хоста виртуальной машине целиком: параметры ядра, модули
vfio, привязка устройства, hostpci в конфиге ВМ. Каждое изменение
записывается, откат делается одной командой keel gpu revert.
Если видеокарта одна — хост теряет локальный монитор.
EOF
}

_gpu_configured() { config_bool host.gpu_passthrough.enabled false; }
_gpu_vm()         { config_get host.gpu_passthrough.vm ""; }
_gpu_device()     { config_get host.gpu_passthrough.device auto; }

# Общая часть check и apply: выбрать устройство и проверить предпосылки
_gpu_prepare() {
  local vm; vm=$(_gpu_vm)
  if [[ -z "$vm" ]]; then
    err "Не указано, какой ВМ отдать видеокарту (host.gpu_passthrough.vm)"
    return 1
  fi
  gpu_resolve_device "$(_gpu_device)" || return 1
  return 0
}

mod_check() {
  _gpu_configured || return "$KEEL_RC_SKIP"
  _gpu_prepare || return 1

  local vm; vm=$(_gpu_vm)
  printf 'устройство: %s (%s)\n' "$KEEL_GPU_ADDR" "$KEEL_GPU_IDS"

  local pre rc=0
  pre=$(gpu_preflight) || rc=$?
  printf '%s\n' "$pre" | sed 's/^/  /'
  if (( rc != 0 )); then
    err "Предпосылки для проброса не выполнены — см. выше."
    return 1
  fi

  if gpu_is_only_card; then
    printf 'ВНИМАНИЕ: видеокарта одна, хост останется без локального монитора\n'
  fi

  local pending; pending=$(gpu_pending_changes "$vm")
  if [[ -z "$pending" ]]; then
    return "$KEEL_RC_OK"
  fi
  printf '%s' "$pending"
  return "$KEEL_RC_CHANGES"
}

mod_apply() {
  _gpu_configured || return "$KEEL_RC_SKIP"
  _gpu_prepare || return 1

  gpu_preflight >/dev/null || {
    err "Предпосылки для проброса не выполнены — запусти keel plan и прочитай список."
    return 1
  }

  gpu_apply "$(_gpu_vm)"
}

mod_verify() {
  _gpu_configured || return "$KEEL_RC_SKIP"
  _gpu_prepare >/dev/null 2>&1 || return 1

  local driver
  driver=$(gpu_list | grep "^${KEEL_GPU_ADDR}|" | cut -d'|' -f3)
  if [[ "$driver" == "vfio-pci" ]]; then
    printf '%s держит vfio-pci — устройство готово к пробросу\n' "$KEEL_GPU_ADDR"
    return "$KEEL_RC_OK"
  fi

  printf '%s пока держит драйвер «%s», а нужен vfio-pci\n' "$KEEL_GPU_ADDR" "${driver:-нет}"
  if [[ -f "$(gpu_state_file)" ]]; then
    printf 'изменения применены, но хост ещё не перезагружен\n'
  fi
  return "$KEEL_RC_CHANGES"
}
