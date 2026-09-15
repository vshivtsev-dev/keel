#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: doctor
#
# Отчёт о состоянии хоста. Ничего не меняет — только смотрит и рассказывает.
# Сюда же входит разведка по видеокарте: это те факты, которые нельзя угадать
# по модели процессора, а ошибиться в них дорого.

_doc_section() { printf '\n%s%s%s\n' "$C_BOLD" "$*" "$C_RESET"; }
_doc_row()     { printf '  %s %s\n' "$(pad "$1" 28)" "$2"; }
_doc_ok()      { printf '  %s✓%s %s %s\n' "$C_GREEN"  "$C_RESET" "$(pad "$1" 26)" "$2"; }
_doc_warn()    { printf '  %s!%s %s %s\n' "$C_YELLOW" "$C_RESET" "$(pad "$1" 26)" "$2"; }
_doc_bad()     { printf '  %s✗%s %s %s\n' "$C_RED"    "$C_RESET" "$(pad "$1" 26)" "$2"; }

doctor_environment() {
  _doc_section "Окружение keel"
  _doc_row "Версия"      "$KEEL_VERSION"
  _doc_row "Каталог"     "$KEEL_ROOT"
  _doc_row "Лог"         "${KEEL_LOG_FILE:-—}"
  _doc_row "Состояние"   "$KEEL_STATE_DIR"

  if [[ "$(id -u)" -eq 0 ]]; then
    _doc_ok "Права" "root"
  else
    _doc_warn "Права" "не root — менять систему не получится, смотреть можно"
  fi

  if command -v whiptail >/dev/null 2>&1; then
    _doc_ok "whiptail" "есть, меню доступно"
  else
    _doc_warn "whiptail" "нет, интерфейс будет текстовым (apt install whiptail)"
  fi

  if config_parser_available; then
    if config_relaxed_supported; then
      _doc_ok "Парсер манифеста" "JSON::PP, режим relaxed — комментарии работают"
    else
      _doc_warn "Парсер манифеста" "JSON::PP без relaxed — комментарии срезаются запасным способом"
    fi
  else
    _doc_bad "Парсер манифеста" "нет Perl с JSON::PP — манифест читать нечем"
  fi

  if [[ -n "$KEEL_MANIFEST" && -f "$KEEL_MANIFEST" ]]; then
    if config_validate >/dev/null 2>&1; then
      _doc_ok "Манифест" "$KEEL_MANIFEST"
    else
      _doc_bad "Манифест" "${KEEL_MANIFEST} — есть ошибки, см. keel validate"
    fi
  else
    _doc_warn "Манифест" "не найден — модули останутся ненастроенными"
    _doc_row "" "создать: cp manifest/host.example.json manifest/host.json"
  fi
}

doctor_host() {
  _doc_section "Хост"
  if pve_is_host; then
    _doc_ok "Proxmox VE" "$(pve_version_full)"
  else
    _doc_bad "Proxmox VE" "не обнаружен — keel рассчитан на хост PVE"
  fi
  _doc_row "Debian"        "$(deb_codename)"
  _doc_row "Процессор"     "$(host_cpu_model) [$(host_cpu_vendor)]"
  _doc_row "Загрузчик"     "$(pve_bootloader)"
  _doc_row "Формат репозиториев" "$(pve_repo_style)"

  local bridges; bridges=$(pve_bridges | paste -sd' ' -)
  _doc_row "Сетевые мосты" "${bridges:-—}"

  local storages; storages=$(pve_storages)
  if [[ -n "$storages" ]]; then
    _doc_section "Хранилища"
    printf '%s\n' "$storages" | sed 's/^/  /'
  fi
}

# Разведка по видеокарте. Три вопроса, ответы на которые нельзя угадать:
# в какой IOMMU-группе сидит карта, чем грузится хост, и есть ли /dev/dri.
doctor_graphics() {
  _doc_section "Графика"

  local gpus; gpus=$(gpu_list)
  if [[ -z "$gpus" ]]; then
    _doc_warn "Видеоустройства" "не найдены (нет lspci?)"
    return 0
  fi

  local count=0 addr desc drv group members
  while IFS='|' read -r addr desc drv; do
    [[ -n "$addr" ]] || continue
    count=$(( count + 1 ))
    printf '  %s%s%s\n' "$C_BOLD" "$desc" "$C_RESET"
    _doc_row "  PCI-адрес" "$addr"
    _doc_row "  Драйвер хоста" "$drv"
    if group=$(iommu_group_of "$addr"); then
      members=$(iommu_group_members "$group" | paste -sd' ' -)
      _doc_row "  IOMMU-группа" "$group"
      local n_members; n_members=$(iommu_group_members "$group" | wc -l)
      if (( n_members <= 2 )); then
        _doc_row "  Соседи по группе" "${members} (изолирована)"
      else
        _doc_row "  Соседи по группе" "$members"
        printf '  %s!%s группа делится с другими устройствами — проброс потянет их за собой\n' \
          "$C_YELLOW" "$C_RESET"
      fi
    else
      _doc_row "  IOMMU-группа" "нет (IOMMU выключен)"
    fi
  done <<< "$gpus"

  _doc_section "Что из этого следует"
  if host_iommu_enabled; then
    _doc_ok "IOMMU" "включён"
  else
    _doc_warn "IOMMU" "выключен — проброс невозможен без правки BIOS и загрузчика"
  fi

  local nodes; nodes=$(dri_nodes | paste -sd' ' -)
  if [[ -n "$nodes" ]]; then
    _doc_ok "Вариант A (LXC + /dev/dri)" "доступен: ${nodes}"
  else
    _doc_warn "Вариант A (LXC + /dev/dri)" "узлов /dev/dri нет — драйвер хоста не поднял видеокарту"
  fi

  if host_has_egl; then
    _doc_ok "Вариант B (ВМ + virtio-gl)" "libEGL на месте"
  else
    _doc_warn "Вариант B (ВМ + virtio-gl)" "нет libEGL — 3D в ВМ не заработает"
  fi

  local vgid rgid
  vgid=$(host_group_gid video); rgid=$(host_group_gid render)
  if [[ -n "$vgid" || -n "$rgid" ]]; then
    _doc_row "Группы на хосте" "video=${vgid:-—} render=${rgid:-—}"
    _doc_row "" "внутри контейнера номера свои; keel разберётся сам"
  fi

  if (( count <= 1 )); then
    _doc_warn "Вариант C (полный проброс)" "видеокарта одна: хост останется без локального монитора"
  else
    _doc_row "Вариант C (полный проброс)" "видеокарт несколько — консоль хоста можно сохранить"
  fi
}

doctor_journal() {
  _doc_section "Последние действия"
  state_tail 10 | sed 's/^/  /'
}

doctor_run() {
  head1 "keel doctor — отчёт о состоянии"
  note "Только чтение: ни одна проверка ничего не меняет."
  doctor_environment
  doctor_host
  doctor_graphics
  doctor_journal
  printf '\n'
  note "Полный лог: ${KEEL_LOG_FILE}"
}
