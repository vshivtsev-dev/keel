#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: гости
#
# Создание виртуальных машин и контейнеров по описанию из манифеста.
# Один гость = одна запись в guests[] + профиль в profiles/.
#
# Железное правило: существующий гость НЕ ТРОГАЕТСЯ. Совпал id — keel
# сообщает об этом и проходит мимо. Ни перезаписи, ни «приведения в
# соответствие», ни удаления. Виртуалка с данными дороже любой стройности.

KEEL_IMAGE_PATH=""     # сюда image_ensure кладёт путь к готовому образу
KEEL_GUEST_SECRET=""   # сгенерированный пароль, если он понадобился

guests_count() { config_len guests; }

# --- Разрешение профиля ------------------------------------------------------

# Профиль «desktop» — это роль, а не реализация. Чем её закрыть, решает
# поле graphics: dri — контейнер с видеокартой хоста, virgl — ВМ с 3D.
guest_profile_name() {
  local i=$1 profile graphics
  profile=$(config_get "guests.${i}.profile" "")
  [[ "$profile" == "desktop" ]] || { printf '%s' "$profile"; return 0; }

  graphics=$(config_get "guests.${i}.graphics" "dri")
  case "$graphics" in
    dri)         printf 'desktop-lxc' ;;
    virgl)       printf 'desktop-vm' ;;
    passthrough) printf 'desktop-vm' ;;   # проброс припаркован, см. docs/BACKLOG.md
    *)           printf '' ;;
  esac
}

# Значение для гостя: сначала манифест, потом умолчание профиля
guest_field() {
  local i=$1 key=$2 default=${3:-} v
  v=$(config_get "guests.${i}.${key}" "")
  [[ -n "$v" ]] && { printf '%s' "$v"; return 0; }
  v=$(prof_get "defaults.${key}" "")
  [[ -n "$v" ]] && { printf '%s' "$v"; return 0; }
  printf '%s' "$default"
}

guest_bool() {
  local v; v=$(guest_field "$1" "$2" "${3:-false}")
  [[ "$v" == "true" || "$v" == "1" ]]
}

# "64G" -> 64 ; "64" -> 64 ; "65536M" -> 64
disk_to_gb() {
  local size=$1 num unit
  num=${size%%[GgMmTt]*}
  unit=${size:${#num}:1}
  [[ -n "$num" ]] || { printf '0'; return 0; }
  case "$unit" in
    M|m) printf '%s' $(( num / 1024 )) ;;
    T|t) printf '%s' $(( num * 1024 )) ;;
    *)   printf '%s' "$num" ;;
  esac
}

# --- Образы ------------------------------------------------------------------

image_cache_dir() { printf '%s' "${KEEL_STATE_DIR}/images"; }

# Последний тег релиза на GitHub. Только чтение; если не вышло — пусто.
github_latest_tag() {
  local repo=$1
  command -v curl >/dev/null 2>&1 || return 0
  curl -fsSL --max-time 15 "https://api.github.com/repos/${repo}/releases/latest" 2>/dev/null \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1
}

# Версия образа: либо явная, либо последний релиз, либо запасная из профиля
image_version() {
  local version; version=$(prof_get image.version "")
  [[ "$version" != "latest" ]] && { printf '%s' "$version"; return 0; }

  local repo tag
  repo=$(prof_get image.github_repo "")
  if [[ -n "$repo" ]]; then
    tag=$(github_latest_tag "$repo")
    if [[ -n "$tag" ]]; then printf '%s' "$tag"; return 0; fi
    warn "Не удалось спросить у GitHub последнюю версию ${repo} — беру запасную."
  fi
  prof_get image.fallback_version ""
}

image_url() {
  local url tmpl version
  url=$(prof_get image.url "")
  [[ -n "$url" ]] && { printf '%s' "$url"; return 0; }

  tmpl=$(prof_get image.url_template "")
  [[ -n "$tmpl" ]] || return 1
  version=$(image_version)
  [[ -n "$version" ]] || return 1
  printf '%s' "${tmpl//\$\{version\}/$version}"
}

# Скачать образ, если его ещё нет, и распаковать. Путь кладётся в
# KEEL_IMAGE_PATH — возвращать его через stdout нельзя, туда пишет run().
image_ensure() {
  local url=$1 compressed=${2:-none}
  local dir; dir=$(image_cache_dir)
  local archive="${dir}/${url##*/}"
  local image="$archive"

  case "$compressed" in
    xz) image="${archive%.xz}" ;;
    gz) image="${archive%.gz}" ;;
  esac

  if [[ -f "$image" ]]; then
    note "Образ уже в кэше: ${image}"
    KEEL_IMAGE_PATH=$image
    return 0
  fi

  [[ -d "$dir" ]] || run "Создать кэш образов ${dir}" mkdir -p "$dir" || return $?

  if [[ ! -f "$archive" ]]; then
    run "Скачать образ ${url##*/}" \
      curl -fL --progress-bar -o "$archive" "$url" || return $?
  fi

  case "$compressed" in
    xz) run "Распаковать ${archive##*/}" xz -d -k "$archive" || return $? ;;
    gz) run "Распаковать ${archive##*/}" gunzip -k "$archive" || return $? ;;
  esac

  if [[ ! -f "$image" && "$KEEL_MODE" != "dry" ]]; then
    err "Образ не появился по пути ${image}"
    return 1
  fi
  KEEL_IMAGE_PATH=$image
  return 0
}

# --- Ключи и пароли ----------------------------------------------------------

# Открытые ключи для гостя: из файла, указанного в манифесте, либо с хоста
guest_ssh_keys() {
  local i=$1 src file
  file=$(config_get "guests.${i}.cloudinit.ssh_key_file" "")
  if [[ -n "$file" ]]; then
    [[ -f "$file" ]] || { err "Нет файла с ключами: ${file}"; return 1; }
    cat "$file"
    return 0
  fi

  src=$(config_get "guests.${i}.cloudinit.ssh_key_from" "")
  [[ "$src" == "host" ]] || return 0

  local f found=0
  for f in "$HOME/.ssh/authorized_keys" /root/.ssh/authorized_keys \
           "$HOME"/.ssh/id_*.pub /root/.ssh/id_*.pub; do
    [[ -f "$f" ]] || continue
    cat "$f"
    found=1
  done
  (( found )) || warn "На хосте не нашлось открытых ключей — гость останется без ключа."
  return 0
}

# Пароль для входа в рабочий стол. В манифесте паролей нет и не будет:
# спрашиваем, а в неинтерактивном режиме генерируем и сохраняем отдельно.
guest_password() {
  local id=$1 name=$2
  local secret_file="${KEEL_STATE_DIR}/secrets/${id}.txt"

  if [[ -f "$secret_file" ]]; then
    KEEL_GUEST_SECRET=$(cat "$secret_file")
    return 0
  fi

  local pw=""
  if [[ "$KEEL_MODE" == "step" ]]; then
    pw=$(ui_password "Пароль для ${name}" \
      "Пароль пользователя внутри гостя ${id} (${name}).
Пусто — сгенерирую случайный и сохраню в ${secret_file}")
  fi

  if [[ -z "$pw" ]]; then
    pw=$(head -c 18 /dev/urandom | base64 | tr -d '/+=' | head -c 16)
    mkdir -p "$(dirname "$secret_file")"
    printf '%s\n' "$pw" >"$secret_file"
    chmod 600 "$secret_file"
    warn "Сгенерирован пароль для ${name}, сохранён в ${secret_file}"
  fi
  KEEL_GUEST_SECRET=$pw
  return 0
}

# --- cloud-init --------------------------------------------------------------

# Хранилище, куда можно положить сниппет с user-data
snippet_storage() {
  local s
  if s=$(storage_with_snippets); then printf '%s' "$s"; return 0; fi
  return 1
}

# Полный user-data. Раз уж мы отдаём cicustom, штатные ciuser/sshkeys
# игнорируются — значит пользователя и ключи описываем здесь сами.
cloudinit_user_data() {
  local i=$1
  local user keys pkgs n j cmd

  user=$(config_get "guests.${i}.cloudinit.user" "admin")
  keys=$(guest_ssh_keys "$i")

  printf '#cloud-config\n'
  printf '# Создано keel для гостя %s\n' "$(config_get "guests.${i}.name")"
  printf 'hostname: %s\n' "$(config_get "guests.${i}.name")"
  printf 'manage_etc_hosts: true\n'
  printf 'users:\n'
  printf '  - name: %s\n' "$user"
  printf '    groups: [adm, sudo, video, render, audio]\n'
  printf '    shell: /bin/bash\n'
  printf '    sudo: ["ALL=(ALL) NOPASSWD:ALL"]\n'
  if [[ -n "$keys" ]]; then
    printf '    ssh_authorized_keys:\n'
    while IFS= read -r k; do
      [[ -n "$k" ]] || continue
      printf '      - %s\n' "$k"
    done <<< "$keys"
  fi
  if [[ -n "$KEEL_GUEST_SECRET" ]]; then
    printf '    lock_passwd: false\n'
    printf '    plain_text_passwd: %s\n' "$KEEL_GUEST_SECRET"
  fi

  printf 'package_update: true\n'
  printf 'packages:\n'
  # Сначала пакеты профиля, затем добавленные в манифесте
  n=$(prof_len packages)
  for (( j = 0; j < n; j++ )); do printf '  - %s\n' "$(prof_get "packages.${j}")"; done
  n=$(config_len "guests.${i}.packages")
  for (( j = 0; j < n; j++ )); do printf '  - %s\n' "$(config_get "guests.${i}.packages.${j}")"; done

  n=$(prof_len runcmd)
  local m; m=$(config_len "guests.${i}.runcmd")
  if (( n > 0 || m > 0 )); then
    printf 'runcmd:\n'
    for (( j = 0; j < n; j++ )); do
      cmd=$(prof_get "runcmd.${j}")
      printf '  - %s\n' "$cmd"
    done
    for (( j = 0; j < m; j++ )); do
      cmd=$(config_get "guests.${i}.runcmd.${j}")
      printf '  - %s\n' "$cmd"
    done
  fi
}

# --- Сборка аргументов -------------------------------------------------------

# Общая часть для qm create. Результат — в массиве KEEL_ARGS.
_vm_base_args() {
  local i=$1
  local name storage bridge
  name=$(config_get "guests.${i}.name")
  bridge=$(guest_field "$i" bridge vmbr0)

  KEEL_ARGS=(
    --name    "$name"
    --memory  "$(guest_field "$i" memory 2048)"
    --cores   "$(guest_field "$i" cores 2)"
    --cpu     "$(prof_get vm.cpu host)"
    --machine "$(prof_get vm.machine q35)"
    --bios    "$(prof_get vm.bios seabios)"
    --scsihw  "$(prof_get vm.scsihw virtio-scsi-single)"
    --ostype  "$(prof_get vm.ostype l26)"
    --net0    "virtio,bridge=${bridge}"
  )
  prof_bool vm.agent      && KEEL_ARGS+=(--agent 1)
  guest_bool "$i" start_on_boot && KEEL_ARGS+=(--onboot 1)

  local vga audio
  vga=$(prof_get vm.vga "")
  [[ -n "$vga" ]] && KEEL_ARGS+=(--vga "$vga")
  audio=$(prof_get vm.audio "")
  [[ -n "$audio" && "$audio" != "false" ]] && KEEL_ARGS+=(--audio0 "device=${audio}")
  if prof_bool vm.serial; then KEEL_ARGS+=(--serial0 socket); fi
  return 0
}

# --- Виртуальная машина из готового образа (Home Assistant OS) ---------------

vm_create_from_image() {
  local i=$1
  local id name storage disk url
  id=$(config_get "guests.${i}.id")
  name=$(config_get "guests.${i}.name")
  storage=$(guest_field "$i" storage local-lvm)
  disk=$(guest_field "$i" disk "")

  url=$(image_url) || { err "В профиле ${KEEL_PROF_NAME} не собирается ссылка на образ"; return 1; }
  image_ensure "$url" "$(prof_get image.compressed none)" || return $?

  _vm_base_args "$i"
  run "Создать виртуальную машину ${id} (${name})" qm create "$id" "${KEEL_ARGS[@]}" || return $?

  if prof_bool vm.efidisk; then
    local keys=0
    prof_bool vm.pre_enrolled_keys && keys=1
    run "Добавить EFI-диск" \
      qm set "$id" --efidisk0 "${storage}:0,efitype=4m,pre-enrolled-keys=${keys}" || return $?
  fi

  # import-from создаёт диск и заливает в него образ одной командой:
  # не нужно разбирать вывод импорта, чтобы узнать имя тома
  run "Импортировать образ в диск ВМ" \
    qm set "$id" --scsi0 "${storage}:0,import-from=${KEEL_IMAGE_PATH},discard=on,ssd=1" || return $?
  run "Настроить порядок загрузки" qm set "$id" --boot order=scsi0 || return $?

  if [[ -n "$disk" ]]; then
    run "Расширить диск до ${disk}" qm resize "$id" scsi0 "$disk" \
      || warn "Расширить диск до ${disk} не вышло (образ уже больше?) — ВМ создана, поправь вручную."
  fi

  guest_bool "$i" start false && run "Запустить ВМ ${id}" qm start "$id"
  return 0
}

# --- Виртуальная машина из облачного образа (cloud-init) ---------------------

vm_create_cloudinit() {
  local i=$1
  local id name storage disk url snipstore snippath snipfile
  id=$(config_get "guests.${i}.id")
  name=$(config_get "guests.${i}.name")
  storage=$(guest_field "$i" storage local-lvm)
  disk=$(guest_field "$i" disk "")

  if ! snipstore=$(snippet_storage); then
    err "Ни у одного хранилища не разрешены сниппеты, а без них cloud-init не настроить."
    note 'Добавь "snippets" в content хранилища local — модуль 30-storage это сделает.'
    return 1
  fi
  snippath=$(storage_path "$snipstore")
  [[ -n "$snippath" ]] || { err "Не удалось узнать каталог хранилища ${snipstore}"; return 1; }
  snipfile="keel-${id}-user.yml"

  prof_bool needs_password && { guest_password "$id" "$name" || return $?; }

  url=$(image_url) || { err "В профиле ${KEEL_PROF_NAME} нет ссылки на образ"; return 1; }
  image_ensure "$url" "$(prof_get image.compressed none)" || return $?

  _vm_base_args "$i"
  run "Создать виртуальную машину ${id} (${name})" qm create "$id" "${KEEL_ARGS[@]}" || return $?

  run "Импортировать облачный образ в диск ВМ" \
    qm set "$id" --scsi0 "${storage}:0,import-from=${KEEL_IMAGE_PATH},discard=on,ssd=1" || return $?
  run "Подключить диск cloud-init" qm set "$id" --ide2 "${storage}:cloudinit" || return $?
  run "Настроить порядок загрузки" qm set "$id" --boot order=scsi0 || return $?

  cloudinit_user_data "$i" \
    | run_write "Записать настройки первого запуска (cloud-init)" \
        "${snippath}/snippets/${snipfile}" || return $?
  run "Указать ВМ файл настроек первого запуска" \
    qm set "$id" --cicustom "user=${snipstore}:snippets/${snipfile}" || return $?

  local ipcfg; ipcfg=$(config_get "guests.${i}.cloudinit.ipconfig" "ip=dhcp")
  run "Настроить сеть гостя (${ipcfg})" qm set "$id" --ipconfig0 "$ipcfg" || return $?

  if [[ -n "$disk" ]]; then
    run "Расширить диск до ${disk}" qm resize "$id" scsi0 "$disk" \
      || warn "Расширить диск до ${disk} не вышло — ВМ создана, поправь вручную."
  fi

  guest_bool "$i" start false && run "Запустить ВМ ${id}" qm start "$id"
  return 0
}

# --- Контейнер LXC -----------------------------------------------------------

# Имя шаблона: если подходящий уже скачан — берём его, иначе самый свежий
# из доступных. Результат в KEEL_LXC_TEMPLATE, скачивание — через run().
lxc_ensure_template() {
  local pattern=$1 storage=$2
  local have want

  have=$(pveam_downloaded | grep -E "$pattern" | tail -n1 || true)
  if [[ -n "$have" ]]; then
    KEEL_LXC_TEMPLATE=${have##*/}
    note "Шаблон уже скачан: ${KEEL_LXC_TEMPLATE}"
    return 0
  fi

  run "Обновить список шаблонов контейнеров" pveam update || return $?
  want=$(pveam_available "$pattern" | tail -n1 || true)
  if [[ -z "$want" ]]; then
    if [[ "$KEEL_MODE" == "dry" ]]; then
      KEEL_LXC_TEMPLATE="${pattern}_amd64.tar.zst"
      note "Шаблон по образцу «${pattern}» определится при настоящем запуске"
      return 0
    fi
    err "Среди доступных шаблонов нет подходящего под «${pattern}»"
    return 1
  fi
  run "Скачать шаблон ${want}" pveam download "$storage" "$want" || return $?
  KEEL_LXC_TEMPLATE=$want
  return 0
}

# Сценарий первичной настройки внутри контейнера.
# Пароль в него попадает, поэтому файл создаётся с правами 600, а на экран
# и в лог уходит только его версия с замаскированным паролем.
_lxc_post_install_script() {
  local i=$1 user=$2 password=$3
  local n j

  printf '#!/usr/bin/env bash\n'
  printf '# Создано keel. Первичная настройка контейнера.\n'
  printf 'set -euo pipefail\n'
  printf 'export DEBIAN_FRONTEND=noninteractive\n'
  printf 'apt-get update\n'

  local pkgs=()
  n=$(prof_len packages)
  for (( j = 0; j < n; j++ )); do pkgs+=("$(prof_get "packages.${j}")"); done
  n=$(config_len "guests.${i}.packages")
  for (( j = 0; j < n; j++ )); do pkgs+=("$(config_get "guests.${i}.packages.${j}")"); done
  if (( ${#pkgs[@]} )); then
    printf 'apt-get install -y --no-install-recommends %s\n' "${pkgs[*]}"
  fi

  printf 'id -u %s >/dev/null 2>&1 || adduser --disabled-password --gecos "" %s\n' "$user" "$user"
  printf 'printf "%%s:%%s" "%s" "%s" | chpasswd\n' "$user" "$password"
  # Группы, через которые внутри контейнера виден /dev/dri
  printf 'for g in sudo video render audio; do getent group "$g" >/dev/null && adduser %s "$g" || true; done\n' "$user"

  n=$(prof_len runcmd)
  for (( j = 0; j < n; j++ )); do printf '%s\n' "$(prof_get "runcmd.${j}")"; done
  n=$(config_len "guests.${i}.runcmd")
  for (( j = 0; j < n; j++ )); do printf '%s\n' "$(config_get "guests.${i}.runcmd.${j}")"; done
}

lxc_post_install() {
  local i=$1 id=$2 user=$3 password=$4
  local script preview
  script=$(mktemp); chmod 600 "$script"
  _lxc_post_install_script "$i" "$user" "$password" >"$script"

  preview=$(sed "s/${password//\//\\/}/********/g" "$script")
  info "Внутри контейнера будет выполнено:"
  printf '%s\n' "$preview" | sed 's/^/    /'

  local rc=0
  run "Загрузить сценарий настройки в контейнер" \
    pct push "$id" "$script" /root/keel-post-install.sh --perms 700 || rc=$?
  if (( rc == 0 )); then
    run "Выполнить первичную настройку внутри контейнера" \
      pct exec "$id" -- bash /root/keel-post-install.sh || rc=$?
  fi
  rm -f "$script"
  return "$rc"
}

lxc_create() {
  local i=$1
  local id name storage bridge disk user tmplstore
  id=$(config_get "guests.${i}.id")
  name=$(config_get "guests.${i}.name")
  storage=$(guest_field "$i" storage local-lvm)
  bridge=$(guest_field "$i" bridge vmbr0)
  disk=$(disk_to_gb "$(guest_field "$i" disk 32G)")
  user=$(config_get "guests.${i}.cloudinit.user" "$(config_get "guests.${i}.user" admin)")
  tmplstore=$(prof_get template.storage local)

  guest_password "$id" "$name" || return $?
  lxc_ensure_template "$(prof_get template.pattern)" "$tmplstore" || return $?

  local args=(
    --hostname "$name"
    --cores    "$(guest_field "$i" cores 2)"
    --memory   "$(guest_field "$i" memory 2048)"
    --swap     "$(prof_get lxc.swap 512)"
    --rootfs   "${storage}:${disk}"
    --net0     "name=eth0,bridge=${bridge},ip=$(config_get "guests.${i}.ip" dhcp)"
    --ostype   "$(prof_get lxc.ostype ubuntu)"
  )
  prof_bool lxc.unprivileged true && args+=(--unprivileged 1)
  local features; features=$(prof_get lxc.features "")
  [[ -n "$features" ]] && args+=(--features "$features")
  guest_bool "$i" start_on_boot && args+=(--onboot 1)

  run "Создать контейнер ${id} (${name})" \
    pct create "$id" "${tmplstore}:vztmpl/${KEEL_LXC_TEMPLATE}" "${args[@]}" || return $?

  if prof_bool lxc.dri; then
    lxc_attach_dri "$id" || return $?
  fi

  run "Запустить контейнер ${id}" pct start "$id" || return $?
  lxc_post_install "$i" "$id" "$user" "$KEEL_GUEST_SECRET" || return $?

  ok "Контейнер ${name} готов. Вход по RDP: пользователь ${user}."
  return 0
}

# Отдать контейнеру видеокарту хоста. Ключи dev0..devN появились в PVE 8.2;
# на более старых версиях это делается правкой конфига руками — см. docs.
lxc_attach_dri() {
  local id=$1
  local nodes render card
  nodes=$(dri_nodes)
  if [[ -z "$nodes" ]]; then
    warn "На хосте нет /dev/dri — видеокарту в контейнер не отдать."
    note "Проверь 'keel doctor': драйвер хоста должен видеть видеокарту."
    return 0
  fi

  if ! pct_supports_dev_keys; then
    err "Этот Proxmox не поддерживает ключи dev0 (нужен PVE 8.2+)."
    note "Способ для старых версий описан в docs/30-desktop.md."
    return 1
  fi

  render=$(printf '%s\n' "$nodes" | grep -m1 'renderD' || true)
  card=$(printf '%s\n' "$nodes" | grep -m1 '/card' || true)

  local n=0
  if [[ -n "$render" ]]; then
    run "Отдать контейнеру ${render}" \
      pct set "$id" "--dev${n}" "${render},gid=$(prof_get lxc.render_gid 993)" || return $?
    n=$(( n + 1 ))
  fi
  if [[ -n "$card" ]]; then
    run "Отдать контейнеру ${card}" \
      pct set "$id" "--dev${n}" "${card},gid=$(prof_get lxc.video_gid 44)" || return $?
  fi
  return 0
}

# --- Общий вход: план, применение, проверка ---------------------------------

# Загрузить профиль гостя и проверить, что описание вообще осмысленно
guest_prepare() {
  local i=$1 profile
  profile=$(guest_profile_name "$i")
  if [[ -z "$profile" ]]; then
    err "guests[${i}]: не понял, какой профиль использовать (profile/graphics)"
    return 1
  fi
  profile_load "$profile" || return 1
  return 0
}

guest_plan() {
  local i=$1
  local id name kind
  id=$(config_get "guests.${i}.id")
  name=$(config_get "guests.${i}.name")
  kind=$(prof_get kind "")

  case "$kind" in
    vm-image)     printf 'создать ВМ %s «%s» из образа (%s)\n' "$id" "$name" "$(prof_get title "$KEEL_PROF_NAME")" ;;
    vm-cloudinit) printf 'создать ВМ %s «%s» из облачного образа (%s)\n' "$id" "$name" "$(prof_get title "$KEEL_PROF_NAME")" ;;
    lxc)          printf 'создать контейнер %s «%s» (%s)\n' "$id" "$name" "$(prof_get title "$KEEL_PROF_NAME")" ;;
    *)            printf 'неизвестный вид гостя «%s» в профиле %s\n' "$kind" "$KEEL_PROF_NAME"; return 1 ;;
  esac

  printf '  ресурсы: %s ядер, %s МБ, диск %s, хранилище %s, мост %s\n' \
    "$(guest_field "$i" cores 2)" "$(guest_field "$i" memory 2048)" \
    "$(guest_field "$i" disk '—')" "$(guest_field "$i" storage local-lvm)" \
    "$(guest_field "$i" bridge vmbr0)"
  return 0
}

guest_apply() {
  local i=$1 kind
  kind=$(prof_get kind "")
  case "$kind" in
    vm-image)     vm_create_from_image "$i" ;;
    vm-cloudinit) vm_create_cloudinit "$i" ;;
    lxc)          lxc_create "$i" ;;
    *)            err "Неизвестный вид гостя «${kind}» в профиле ${KEEL_PROF_NAME}"; return 1 ;;
  esac
}

# Совпадает ли то, что на хосте, с тем, что описано. Существующего гостя
# keel не правит — только сообщает о расхождениях.
guest_report_drift() {
  local i=$1 id=$2
  local conf want have
  if [[ -f "$(fsroot "/etc/pve/qemu-server/${id}.conf")" ]]; then
    conf=$(fsroot "/etc/pve/qemu-server/${id}.conf")
  elif [[ -f "$(fsroot "/etc/pve/lxc/${id}.conf")" ]]; then
    conf=$(fsroot "/etc/pve/lxc/${id}.conf")
  else
    return 0
  fi

  want=$(guest_field "$i" cores "")
  have=$(sed -n 's/^cores:[[:space:]]*//p' "$conf" | head -n1)
  [[ -n "$want" && -n "$have" && "$want" != "$have" ]] \
    && printf '  ядер: на хосте %s, в манифесте %s\n' "$have" "$want"

  want=$(guest_field "$i" memory "")
  have=$(sed -n 's/^memory:[[:space:]]*//p' "$conf" | head -n1)
  [[ -n "$want" && -n "$have" && "$want" != "$have" ]] \
    && printf '  память: на хосте %s, в манифесте %s\n' "$have" "$want"
  return 0
}
