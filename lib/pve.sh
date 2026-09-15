#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: факты о хосте
#
# Всё в этом файле — только чтение. Ни одна функция отсюда ничего не меняет,
# поэтому их можно свободно вызывать из check/verify и из doctor.

pve_is_host() {
  [[ -f /etc/pve/.version ]] || command -v pveversion >/dev/null 2>&1
}

pve_version_full() {
  if command -v pveversion >/dev/null 2>&1; then
    pveversion 2>/dev/null | head -n1
  else
    printf 'не Proxmox VE'
  fi
}

# Мажорная версия PVE: 8, 9, ... Пусто, если определить не удалось.
pve_major() {
  local v
  v=$(pveversion 2>/dev/null | head -n1 | sed -n 's|.*pve-manager/\([0-9]\+\)\..*|\1|p')
  printf '%s' "$v"
}

deb_codename() {
  # shellcheck disable=SC1091
  [[ -r /etc/os-release ]] && . /etc/os-release
  printf '%s' "${VERSION_CODENAME:-}"
}

# Как на этом хосте оформлены репозитории apt:
#   deb822  — новый формат (*.sources), PVE 9 / Debian 13
#   list    — классический (*.list), PVE 8 / Debian 12
pve_repo_style() {
  if compgen -G "$(fsroot /etc/apt/sources.list.d)/*.sources" >/dev/null 2>&1; then
    printf 'deb822'
  elif compgen -G "$(fsroot /etc/apt/sources.list.d)/*.list" >/dev/null 2>&1; then
    printf 'list'
  else
    local major; major=$(pve_major)
    if [[ -n "$major" ]] && (( major >= 9 )); then printf 'deb822'; else printf 'list'; fi
  fi
}

# Чем грузится хост. Важно для проброса GPU: на ZFS это systemd-boot,
# и правка /etc/default/grub там не делает ничего — классическая ловушка.
pve_bootloader() {
  if command -v proxmox-boot-tool >/dev/null 2>&1 \
     && proxmox-boot-tool status >/dev/null 2>&1; then
    if proxmox-boot-tool status 2>/dev/null | grep -qi 'systemd-boot'; then
      printf 'systemd-boot (через proxmox-boot-tool)'
      return
    fi
    printf 'grub (через proxmox-boot-tool)'
    return
  fi
  if [[ -f /etc/default/grub ]]; then printf 'grub'; else printf 'неизвестно'; fi
}

# Включён ли IOMMU прямо сейчас (а не "должен быть включён в BIOS")
host_iommu_enabled() {
  compgen -G "/sys/class/iommu/*" >/dev/null 2>&1 || return 1
  return 0
}

host_cpu_vendor() {
  if grep -qi 'AuthenticAMD' /proc/cpuinfo 2>/dev/null; then printf 'AMD'
  elif grep -qi 'GenuineIntel' /proc/cpuinfo 2>/dev/null; then printf 'Intel'
  else printf 'неизвестно'; fi
}

host_cpu_model() {
  sed -n 's/^model name[[:space:]]*:[[:space:]]*//p' /proc/cpuinfo 2>/dev/null | head -n1
}

# Видеоустройства: "PCI-адрес|описание|активный драйвер"
gpu_list() {
  command -v lspci >/dev/null 2>&1 || return 0
  local line addr desc drv
  while IFS= read -r line; do
    addr=${line%% *}
    desc=${line#* }
    drv=$(lspci -k -s "$addr" 2>/dev/null | sed -n 's/.*Kernel driver in use: //p')
    printf '%s|%s|%s\n' "$addr" "$desc" "${drv:-нет}"
  done < <(lspci -mm 2>/dev/null | grep -Ei 'VGA compatible|Display controller|3D controller' \
           | sed 's/"//g' | awk '{addr=$1; $1=""; sub(/^ /,""); print addr" "$0}')
}

# В какой IOMMU-группе сидит устройство. Пусто — группы нет (IOMMU выключен).
iommu_group_of() {
  local addr=$1 path
  for path in /sys/kernel/iommu_groups/*/devices/*"${addr}"; do
    [[ -e "$path" ]] || continue
    path=${path%/devices/*}
    printf '%s' "${path##*/}"
    return 0
  done
  return 1
}

# Кто ещё сидит в той же группе — решает, можно ли пробросить карту отдельно
iommu_group_members() {
  local group=$1 dev
  for dev in /sys/kernel/iommu_groups/"${group}"/devices/*; do
    [[ -e "$dev" ]] || continue
    printf '%s\n' "$(basename "$dev")"
  done
}

# Узлы прямого доступа к видеокарте — нужны для LXC с общим iGPU
dri_nodes() {
  compgen -G "/dev/dri/*" >/dev/null 2>&1 || return 0
  local n
  for n in /dev/dri/*; do printf '%s\n' "$n"; done
}

# Библиотеки, без которых не заработает 3D-ускорение в ВМ (virtio-gl)
host_has_egl() {
  compgen -G "/usr/lib/*/libEGL.so*" >/dev/null 2>&1 \
    || compgen -G "/usr/lib/libEGL.so*" >/dev/null 2>&1
}

pve_bridges() {
  command -v ip >/dev/null 2>&1 || return 0
  ip -o link show type bridge 2>/dev/null | awk -F': ' '{print $2}'
}

pve_storages() {
  command -v pvesm >/dev/null 2>&1 || return 0
  pvesm status 2>/dev/null | tail -n +2
}

# Существует ли гость с таким ID (ВМ или контейнер) — понадобится в Фазе 2
guest_exists() {
  local id=$1
  [[ -f "/etc/pve/qemu-server/${id}.conf" || -f "/etc/pve/lxc/${id}.conf" ]]
}
