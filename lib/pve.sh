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
  local dir; dir=$(fsroot /dev/dri)
  compgen -G "${dir}/*" >/dev/null 2>&1 || return 0
  local n
  for n in "${dir}"/*; do printf '%s\n' "$n"; done
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
  [[ -f "$(fsroot "/etc/pve/qemu-server/${id}.conf")" \
     || -f "$(fsroot "/etc/pve/lxc/${id}.conf")" ]]
}

# --- Пакеты ------------------------------------------------------------------

# Сколько пакетов ждёт обновления. apt-get -s только моделирует и ничего
# не меняет, поэтому живёт здесь, среди фактов о хосте.
apt_upgradable_count() {
  command -v apt-get >/dev/null 2>&1 || { printf '0'; return 0; }
  local n
  n=$(LC_ALL=C apt-get -s dist-upgrade 2>/dev/null | grep -c '^Inst ' || true)
  printf '%s' "${n:-0}"
}

apt_upgradable_list() {
  command -v apt-get >/dev/null 2>&1 || return 0
  LC_ALL=C apt-get -s dist-upgrade 2>/dev/null | sed -n 's/^Inst \([^ ]*\) .*/\1/p'
}

# --- Хранилища ---------------------------------------------------------------

_storage_cfg() { fsroot /etc/pve/storage.cfg; }

storage_exists() {
  local name=$1 cfg; cfg=$(_storage_cfg)
  [[ -f "$cfg" ]] || return 1
  grep -qE "^[a-z]+:[[:space:]]+${name}\$" "$cfg"
}

# Тип хранилища: dir, lvmthin, zfspool ...
storage_type() {
  local name=$1 cfg; cfg=$(_storage_cfg)
  [[ -f "$cfg" ]] || return 1
  sed -n "s/^\([a-z]\+\):[[:space:]]\+${name}\$/\1/p" "$cfg" | head -n1
}

# Значение поля внутри блока хранилища (content, path, ...)
storage_field() {
  local name=$1 field=$2 cfg; cfg=$(_storage_cfg)
  [[ -f "$cfg" ]] || return 1
  awk -v name="$name" -v field="$field" '
    /^[a-z]+:[[:space:]]+/ { inblock = ($2 == name) }
    inblock && $1 == field { $1=""; sub(/^[[:space:]]+/,""); print; exit }
  ' "$cfg"
}

storage_content() { storage_field "$1" content; }

# Каталог dir-хранилища — туда кладутся образы, шаблоны и сниппеты
storage_path() { storage_field "$1" path; }

# Хранилище, у которого разрешены сниппеты (нужны для cloud-init)
storage_with_snippets() {
  local cfg; cfg=$(_storage_cfg)
  [[ -f "$cfg" ]] || return 1
  local name
  while IFS= read -r name; do
    [[ -n "$name" ]] || continue
    if [[ ",$(storage_content "$name")," == *",snippets,"* ]]; then
      printf '%s' "$name"; return 0
    fi
  done < <(sed -n 's/^[a-z]\+:[[:space:]]\+\(.*\)$/\1/p' "$cfg")
  return 1
}

# --- Возможности установленного Proxmox --------------------------------------

# В PVE 8 появился «qm disk import»; старый «qm importdisk» ещё жив, но
# импорт одной командой через import-from надёжнее: не надо разбирать вывод.
qm_supports_import_from() {
  command -v qm >/dev/null 2>&1 || return 1
  local major; major=$(pve_major)
  [[ -n "$major" ]] && (( major >= 8 ))
}

# Проброс устройств в контейнер через ключи dev0..devN (PVE 8.2+).
pct_supports_dev_keys() {
  command -v pct >/dev/null 2>&1 || return 1
  pct help set 2>&1 | grep -q -- '--dev\[n\]\|--dev0'
}

# Шаблоны LXC, уже скачанные на хост
pveam_downloaded() {
  command -v pveam >/dev/null 2>&1 || return 0
  pveam list local 2>/dev/null | awk 'NR>1 {print $1}'
}

# Доступные для скачивания шаблоны, отфильтрованные по образцу
pveam_available() {
  local pattern=$1
  command -v pveam >/dev/null 2>&1 || return 0
  pveam available --section system 2>/dev/null | awk '{print $2}' | grep -E "$pattern" || true
}

# --- Задания резервного копирования ------------------------------------------

# Список заданий vzdump в JSON. Чтение, ничего не меняет.
backup_jobs_json() {
  command -v pvesh >/dev/null 2>&1 || { printf '[]'; return 0; }
  pvesh get /cluster/backup --output-format json 2>/dev/null || printf '[]'
}

# --- Снимок состояния хоста для резервной копии ------------------------------

# Текстовый отчёт: то, что нельзя восстановить из файлов, но очень нужно
# знать при сборке хоста заново. Только чтение.
host_config_report() {
  printf '# Снимок хоста, сделан %s\n\n' "$(date '+%Y-%m-%d %H:%M:%S')"

  printf '## Версия Proxmox\n'
  pveversion -v 2>/dev/null || printf 'pveversion недоступен\n'

  printf '\n## Диски\n'
  lsblk -o NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT 2>/dev/null || true

  printf '\n## Хранилища\n'
  pvesm status 2>/dev/null || true

  printf '\n## Сеть\n'
  ip -o addr show 2>/dev/null || true

  printf '\n## Виртуальные машины\n'
  qm list 2>/dev/null || true

  printf '\n## Контейнеры\n'
  pct list 2>/dev/null || true

  printf '\n## ZFS\n'
  zpool list 2>/dev/null || printf 'ZFS не используется\n'

  printf '\n## Загрузчик\n'
  proxmox-boot-tool status 2>/dev/null || printf 'proxmox-boot-tool недоступен\n'
}

# Что именно кладём в архив. Пути относительно корня, несуществующие
# пропускаются молча.
host_config_paths() {
  local p
  for p in etc/pve \
           etc/network/interfaces etc/network/interfaces.d \
           etc/hosts etc/hostname etc/resolv.conf etc/fstab \
           etc/apt/sources.list etc/apt/sources.list.d \
           etc/default/grub etc/kernel/cmdline \
           etc/modules etc/modprobe.d \
           etc/vzdump.conf etc/ssh/sshd_config etc/ssh/sshd_config.d \
           root/.ssh/authorized_keys; do
    [[ -e "$(fsroot "/${p}")" ]] && printf '%s\n' "$p"
  done
  return 0
}

# Каталог с тем, чего нет в /etc: снимок состояния хоста и сам манифест.
# Печатает путь; удалять его потом через keel_tmp_cleanup.
host_config_staging_dir() {
  local dir; dir=$(mktemp -d)
  host_config_report >"${dir}/host-report.txt" 2>/dev/null || true
  if [[ -n "${KEEL_MANIFEST:-}" && -f "$KEEL_MANIFEST" ]]; then
    cp "$KEEL_MANIFEST" "${dir}/manifest.json"
  fi
  printf 'keel %s, снято %s\n' "${KEEL_VERSION:-?}" "$(date '+%Y-%m-%d %H:%M:%S')" \
    >"${dir}/keel-version.txt"
  printf '%s' "$dir"
}

# Числовой идентификатор группы на хосте (video, render, …)
host_group_gid() {
  getent group "$1" 2>/dev/null | cut -d: -f3
}
