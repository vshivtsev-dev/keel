#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: проброс видеокарты (вариант C)
#
# Отдаёт видеокарту хоста виртуальной машине целиком. Максимальная
# производительность — и максимальный риск: если видеокарта в машине одна,
# хост навсегда теряет локальный монитор, а управление остаётся только через
# веб и SSH.
#
# Поэтому здесь две равные по важности половины: подготовка и откат. Всё, что
# модуль меняет, он записывает в /var/lib/keel/gpu-passthrough.state вместе с
# путями к резервным копиям. Откат идёт по этой записи, а не по догадке.

KEEL_GPU_ADDR=""      # PCI-адрес выбранного устройства
KEEL_GPU_IDS=""       # vendor:device для привязки к vfio-pci

gpu_state_file() { printf '%s' "${KEEL_STATE_DIR}/gpu-passthrough.state"; }
gpu_modprobe_file() { fsroot /etc/modprobe.d/keel-vfio.conf; }
gpu_modules_file()  { fsroot /etc/modules; }

# Куда писать параметры ядра. На PVE с proxmox-boot-tool правка
# /etc/default/grub не делает ничего — это самая частая причина «сделал всё
# по гайду, не завелось».
gpu_cmdline_file() {
  case "$(pve_bootloader)" in
    *proxmox-boot-tool*) fsroot /etc/kernel/cmdline ;;
    *)                   fsroot /etc/default/grub ;;
  esac
}

gpu_cmdline_is_grub() {
  [[ "$(gpu_cmdline_file)" == *"/etc/default/grub" ]]
}

# Параметры ядра под производителя процессора
gpu_kernel_params() {
  if [[ "$(host_cpu_vendor)" == "AMD" ]]; then
    printf 'amd_iommu=on iommu=pt'
  else
    printf 'intel_iommu=on iommu=pt'
  fi
}

# --- Выбор устройства --------------------------------------------------------

# "auto" — единственная видеокарта на хосте; иначе явный PCI-адрес
gpu_resolve_device() {
  local want=$1
  local list; list=$(gpu_list)
  [[ -n "$list" ]] || { err "На хосте не найдено ни одной видеокарты."; return 1; }

  if [[ -n "$want" && "$want" != "auto" ]]; then
    if ! printf '%s\n' "$list" | cut -d'|' -f1 | grep -qx "$want"; then
      err "Устройства ${want} нет среди видеокарт хоста."
      printf '%s\n' "$list" | cut -d'|' -f1-2 | sed 's/^/  /' >&2
      return 1
    fi
    KEEL_GPU_ADDR=$want
  else
    local count; count=$(printf '%s\n' "$list" | grep -c .)
    if (( count > 1 )); then
      err "Видеокарт несколько — выбери явно, какую пробрасывать (host.gpu_passthrough.device)."
      printf '%s\n' "$list" | cut -d'|' -f1-2 | sed 's/^/  /' >&2
      return 1
    fi
    KEEL_GPU_ADDR=$(printf '%s' "$list" | head -n1 | cut -d'|' -f1)
  fi

  KEEL_GPU_IDS=$(gpu_device_ids "$KEEL_GPU_ADDR") || return 1
  [[ -n "$KEEL_GPU_IDS" ]] || { err "Не удалось определить ID устройства ${KEEL_GPU_ADDR}."; return 1; }
  return 0
}

# vendor:device — то, по чему vfio-pci забирает устройство себе
gpu_device_ids() {
  local addr=$1
  command -v lspci >/dev/null 2>&1 || return 1
  lspci -n -s "$addr" 2>/dev/null | awk '{print $3}' | head -n1
}

# --- Проверки до всего -------------------------------------------------------
#
# Лучше отказаться с объяснением, чем сделать «как-нибудь» и оставить хост
# без картинки.

gpu_preflight() {
  local rc=0

  if host_iommu_enabled; then
    printf 'IOMMU включён\n'
  else
    printf 'IOMMU выключен: включи VT-d/AMD-Vi в BIOS, иначе проброс невозможен\n'
    rc=1
  fi

  local group members count
  if group=$(iommu_group_of "$KEEL_GPU_ADDR"); then
    count=$(iommu_group_members "$group" | grep -c .)
    if (( count > 1 )); then
      members=$(iommu_group_members "$group" | paste -sd' ' -)
      printf 'IOMMU-группа %s делится с другими устройствами: %s\n' "$group" "$members"
      printf 'они уйдут в ВМ вместе с видеокартой — это почти наверняка не то, что нужно\n'
      rc=1
    else
      printf 'IOMMU-группа %s: устройство изолировано\n' "$group"
    fi
  else
    printf 'У устройства нет IOMMU-группы — проброс невозможен\n'
    rc=1
  fi

  case "$(pve_bootloader)" in
    неизвестно) printf 'Загрузчик не определён — некуда прописывать параметры ядра\n'; rc=1 ;;
    *)          printf 'Загрузчик: %s\n' "$(pve_bootloader)" ;;
  esac

  return "$rc"
}

# Одна ли это видеокарта на хосте — от этого зависит, потеряет ли хост монитор
gpu_is_only_card() {
  local count; count=$(gpu_list | grep -c .)
  (( count <= 1 ))
}

# --- Желаемое содержимое файлов ---------------------------------------------

# Параметры ядра добавляются к существующим, а не заменяют их
_gpu_cmdline_content() {
  local file=$1 params; params=$(gpu_kernel_params)

  if gpu_cmdline_is_grub; then
    awk -v add="$params" '
      /^[[:space:]]*GRUB_CMDLINE_LINUX_DEFAULT=/ {
        cur = ""
        if (match($0, /"[^"]*"/)) cur = substr($0, RSTART + 1, RLENGTH - 2)
        n = split(add, want, " ")
        for (i = 1; i <= n; i++)
          if (index(" " cur " ", " " want[i] " ") == 0)
            cur = (cur == "" ? want[i] : cur " " want[i])
        print "GRUB_CMDLINE_LINUX_DEFAULT=\"" cur "\""
        seen = 1
        next
      }
      { print }
      END { if (!seen) print "GRUB_CMDLINE_LINUX_DEFAULT=\"" add "\"" }
    ' "$file" 2>/dev/null
  else
    local cur; cur=$(cat "$file" 2>/dev/null | head -n1)
    local p
    for p in $params; do
      [[ " ${cur} " == *" ${p} "* ]] || cur="${cur} ${p}"
    done
    printf '%s\n' "${cur# }"
  fi
}

_gpu_modules_content() {
  local file=$1
  cat "$file" 2>/dev/null
  local m
  for m in vfio vfio_iommu_type1 vfio_pci; do
    grep -qx "$m" "$file" 2>/dev/null || printf '%s\n' "$m"
  done
}

_gpu_modprobe_content() {
  local vm=$1
  cat <<EOF
# Создано keel. Видеокарта ${KEEL_GPU_ADDR} (${KEEL_GPU_IDS}) отдана ВМ ${vm}.
#
# Откатить: keel gpu revert
# Если хост не загрузился — удалить этот файл с live-USB и обновить initramfs.

options vfio-pci ids=${KEEL_GPU_IDS} disable_vga=1

# vfio-pci должен успеть забрать устройство раньше штатного драйвера
softdep amdgpu pre: vfio-pci
softdep radeon pre: vfio-pci
softdep nouveau pre: vfio-pci
softdep nvidia pre: vfio-pci

blacklist amdgpu
blacklist radeon
EOF
}

# --- Что изменится -----------------------------------------------------------

# Печатает список предстоящих изменений; пусто — значит всё уже сделано
gpu_pending_changes() {
  local vm=$1
  local file want

  file=$(gpu_cmdline_file)
  want=$(_gpu_cmdline_content "$file")
  if ! diff -q <(printf '%s\n' "$want") "$file" >/dev/null 2>&1; then
    printf 'параметры ядра (%s): %s\n' "$file" "$(gpu_kernel_params)"
  fi

  file=$(gpu_modules_file)
  want=$(_gpu_modules_content "$file")
  if ! diff -q <(printf '%s\n' "$want") "$file" >/dev/null 2>&1; then
    printf 'модули vfio в %s\n' "$file"
  fi

  file=$(gpu_modprobe_file)
  want=$(_gpu_modprobe_content "$vm")
  if ! diff -q <(printf '%s\n' "$want") "$file" >/dev/null 2>&1; then
    printf 'привязка устройства к vfio-pci (%s)\n' "$file"
  fi

  local conf; conf=$(fsroot "/etc/pve/qemu-server/${vm}.conf")
  if [[ -f "$conf" ]] && ! grep -q "^hostpci0:.*${KEEL_GPU_ADDR}" "$conf" 2>/dev/null; then
    printf 'отдать устройство ВМ %s (hostpci0)\n' "$vm"
  fi
  return 0
}

# --- Применение --------------------------------------------------------------

# Подтверждение, которое нельзя нажать не глядя: нужно набрать адрес устройства
gpu_confirm_blackout() {
  local answer
  answer=$(ui_input "Хост останется без монитора" \
"Видеокарта ${KEEL_GPU_ADDR} на этом хосте одна.

После перезагрузки локальный монитор погаснет НАВСЕГДА. Управление останется
только через веб-интерфейс и SSH. Если ВМ не поднимется, чинить придётся
вслепую или с live-USB.

Чтобы подтвердить, набери адрес устройства: ${KEEL_GPU_ADDR}")

  if [[ "$answer" != "$KEEL_GPU_ADDR" ]]; then
    warn "Не подтверждено — проброс не выполняется."
    return 1
  fi
  return 0
}

_gpu_state_write() {
  local vm=$1 stamp=$2; shift 2
  local changed=() created=() f
  for f in "$@"; do
    case "$f" in
      +*) created+=("${f#+}") ;;
      *)  changed+=("$f") ;;
    esac
  done

  {
    printf '{\n'
    printf '  "created": "%s",\n' "$(date '+%Y-%m-%d %H:%M:%S')"
    printf '  "device": "%s",\n' "$KEEL_GPU_ADDR"
    printf '  "ids": "%s",\n' "$KEEL_GPU_IDS"
    printf '  "vm": "%s",\n' "$vm"
    printf '  "stamp": "%s",\n' "$stamp"
    printf '  "bootloader": "%s",\n' "$(pve_bootloader)"
    printf '  "changed": ['
    local i
    for i in "${!changed[@]}"; do
      (( i > 0 )) && printf ', '
      printf '"%s"' "${changed[$i]}"
    done
    printf '],\n'
    printf '  "created_files": ['
    for i in "${!created[@]}"; do
      (( i > 0 )) && printf ', '
      printf '"%s"' "${created[$i]}"
    done
    printf ']\n}\n'
  } > "$(gpu_state_file)"
  keel_log "GPU: состояние записано в $(gpu_state_file)"
}

# Обновить загрузчик тем способом, который на этом хосте действительно работает
gpu_refresh_boot() {
  if gpu_cmdline_is_grub; then
    run "Обновить конфигурацию загрузчика" update-grub
  else
    run "Обновить конфигурацию загрузчика" proxmox-boot-tool refresh
  fi
}

gpu_apply() {
  local vm=$1
  local stamp; stamp=$(date +%Y-%m-%d_%H%M%S)
  export KEEL_RUN_STAMP="$stamp"   # чтобы все копии легли в один каталог
  local touched=()

  local file want
  file=$(gpu_cmdline_file)
  want=$(_gpu_cmdline_content "$file")
  if ! diff -q <(printf '%s\n' "$want") "$file" >/dev/null 2>&1; then
    [[ -f "$file" ]] && touched+=("$file") || touched+=("+${file}")
    printf '%s\n' "$want" | run_write "Добавить параметры ядра ($(gpu_kernel_params))" "$file" || return $?
  fi

  file=$(gpu_modules_file)
  want=$(_gpu_modules_content "$file")
  if ! diff -q <(printf '%s\n' "$want") "$file" >/dev/null 2>&1; then
    [[ -f "$file" ]] && touched+=("$file") || touched+=("+${file}")
    printf '%s\n' "$want" | run_write "Включить модули vfio" "$file" || return $?
  fi

  # Точка невозврата: дальше устройство уедет от хоста
  if gpu_is_only_card; then
    gpu_confirm_blackout || return 1
  fi

  file=$(gpu_modprobe_file)
  want=$(_gpu_modprobe_content "$vm")
  if ! diff -q <(printf '%s\n' "$want") "$file" >/dev/null 2>&1; then
    [[ -f "$file" ]] && touched+=("$file") || touched+=("+${file}")
    printf '%s\n' "$want" | run_write "Привязать ${KEEL_GPU_ADDR} к vfio-pci" "$file" || return $?
  fi

  gpu_refresh_boot || return $?
  run "Пересобрать initramfs" update-initramfs -u -k all || return $?

  local conf; conf=$(fsroot "/etc/pve/qemu-server/${vm}.conf")
  if [[ -f "$conf" ]]; then
    if ! grep -q "^hostpci0:.*${KEEL_GPU_ADDR}" "$conf" 2>/dev/null; then
      run "Отдать видеокарту ВМ ${vm}" \
        qm set "$vm" --hostpci0 "${KEEL_GPU_ADDR},pcie=1" || return $?
    fi
  else
    warn "ВМ ${vm} ещё нет — видеокарту отдадим после её создания."
    note "Создай гостя (keel apply) и повтори: keel apply --only host/60-gpu-passthrough"
  fi

  _gpu_state_write "$vm" "$stamp" "${touched[@]}"
  gpu_print_revert_instructions "$vm"
  return 0
}

# Печатается ДО перезагрузки: после неё читать будет негде
gpu_print_revert_instructions() {
  local vm=$1
  head1 "Как вернуть всё назад"
  cat <<EOF
  Обычный откат, с работающего хоста:

      keel gpu revert
      reboot

  Если хост не загрузился и монитора нет — с live-USB:

      1. загрузиться с любого live-образа Linux;
      2. смонтировать корневой раздел хоста, например в /mnt;
      3. rm /mnt$(gpu_modprobe_file | sed "s|^${KEEL_FS_ROOT}||")
      4. перезагрузиться.

  Файл с записью обо всех изменениях: $(gpu_state_file)
  Резервные копии изменённых файлов:  ${KEEL_BACKUP_DIR}/

EOF
  warn "Перепиши эти четыре строки на бумагу или в телефон — с погасшего монитора их не прочитать."
}

# --- Откат -------------------------------------------------------------------

gpu_revert() {
  local state; state=$(gpu_state_file)
  if [[ ! -f "$state" ]]; then
    err "Нет записи о пробросе (${state}) — откатывать нечего."
    note "Если правки делались руками, смотри ${KEEL_BACKUP_DIR}/ и docs/30-desktop.md"
    return 1
  fi

  local json; json=$(cat "$state")
  local vm stamp n i path backup
  vm=$(json_get "$json" vm "")
  stamp=$(json_get "$json" stamp "")

  head1 "Откат проброса видеокарты"
  info "Устройство: $(json_get "$json" device '?'), ВМ: ${vm}, сделано: $(json_get "$json" created '?')"

  # Файлы, которые существовали до нас, — вернуть из копий
  n=$(json_len "$json" changed)
  for (( i = 0; i < n; i++ )); do
    path=$(json_get "$json" "changed.${i}")
    backup="${KEEL_BACKUP_DIR}/${stamp}${path}"
    if [[ -f "$backup" ]]; then
      run "Вернуть ${path} из копии" cp -a "$backup" "$path" || return $?
    else
      warn "Нет копии для ${path} (искал ${backup}) — правь руками."
    fi
  done

  # Файлы, которых до нас не было, — удалить
  n=$(json_len "$json" created_files)
  for (( i = 0; i < n; i++ )); do
    path=$(json_get "$json" "created_files.${i}")
    [[ -e "$path" ]] || continue
    run "Удалить ${path}" rm -f "$path" || return $?
  done

  gpu_refresh_boot || return $?
  run "Пересобрать initramfs" update-initramfs -u -k all || return $?

  local conf; conf=$(fsroot "/etc/pve/qemu-server/${vm}.conf")
  if [[ -f "$conf" ]] && grep -q '^hostpci0:' "$conf" 2>/dev/null; then
    run "Забрать видеокарту у ВМ ${vm}" qm set "$vm" --delete hostpci0 || return $?
  fi

  run "Убрать запись о пробросе" rm -f "$state" || return $?

  ok "Откат выполнен. Перезагрузи хост, чтобы видеокарта вернулась драйверу."
  return 0
}
