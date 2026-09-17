#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: репозитории Proxmox
#
# Свежеустановленный PVE смотрит в платный enterprise-репозиторий. Без подписки
# он отвечает 401, и apt update ругается на каждом запуске. Модуль переключает
# хост на тот репозиторий, который выбран в манифесте.
#
# Манифест:  "host": { "repos": "no-subscription" | "enterprise" | "test" }
# Нет ключа — модуль не делает ничего.

mod_title() { printf 'Репозитории Proxmox'; }

mod_describe() {
  cat <<'EOF'
Переключает apt между репозиториями Proxmox: бесплатным (no-subscription),
платным (enterprise) и тестовым (test). Выключенные репозитории не удаляются,
а помечаются выключенными — вернуть обратно можно одной правкой.
EOF
}

# --- Общее для check / apply / verify ---------------------------------------

_repos_target() { config_get host.repos ""; }

_repos_codename() {
  local c; c=$(deb_codename)
  printf '%s' "${c:-bookworm}"
}

_repos_keyring() {
  local k
  for k in /usr/share/keyrings/proxmox-archive-keyring.gpg \
           "/usr/share/keyrings/proxmox-release-$(_repos_codename).gpg"; do
    [[ -f "$k" ]] && { printf '%s' "$k"; return 0; }
  done
  printf '/usr/share/keyrings/proxmox-archive-keyring.gpg'
}

# Путь к файлу выбранного репозитория, в том формате, который принят на хосте
_repos_file() {
  local repo=$1 style; style=$(pve_repo_style)
  if [[ "$style" == "deb822" ]]; then
    fsroot "/etc/apt/sources.list.d/pve-${repo}.sources"
  else
    fsroot "/etc/apt/sources.list.d/pve-${repo}.list"
  fi
}

_repos_uri() {
  case "$1" in
    enterprise) printf 'https://enterprise.proxmox.com/debian/pve' ;;
    *)          printf 'http://download.proxmox.com/debian/pve' ;;
  esac
}

_repos_component() {
  case "$1" in
    no-subscription) printf 'pve-no-subscription' ;;
    enterprise)      printf 'pve-enterprise' ;;
    test)            printf 'pvetest' ;;
  esac
}

_repos_desired_content() {
  local repo=$1 style; style=$(pve_repo_style)
  if [[ "$style" == "deb822" ]]; then
    cat <<EOF
# Создано keel. Репозиторий Proxmox VE: ${repo}
Types: deb
URIs: $(_repos_uri "$repo")
Suites: $(_repos_codename)
Components: $(_repos_component "$repo")
Signed-By: $(_repos_keyring)
EOF
  else
    cat <<EOF
# Создано keel. Репозиторий Proxmox VE: ${repo}
deb $(_repos_uri "$repo") $(_repos_codename) $(_repos_component "$repo")
EOF
  fi
}

# Файлы чужих репозиториев Proxmox, которые надо выключить при смене
_repos_other_files() {
  local target=$1 repo f
  for repo in no-subscription enterprise test; do
    [[ "$repo" == "$target" ]] && continue
    for f in "$(fsroot "/etc/apt/sources.list.d/pve-${repo}.sources")" \
             "$(fsroot "/etc/apt/sources.list.d/pve-${repo}.list")"; do
      [[ -f "$f" ]] && printf '%s\n' "$f"
    done
  done
  # Платный ceph-репозиторий тоже отвечает 401 без подписки
  if [[ "$target" != "enterprise" ]]; then
    for f in "$(fsroot /etc/apt/sources.list.d/ceph.sources)" \
             "$(fsroot /etc/apt/sources.list.d/ceph.list)"; do
      [[ -f "$f" ]] && grep -q 'enterprise\.proxmox\.com' "$f" 2>/dev/null && printf '%s\n' "$f"
    done
  fi
  return 0
}

# --- Выключение репозитория в формате deb822 ----------------------------------
#
# Записи в .sources разделяются пустой строкой, и у каждой обязано быть поле
# Types. Дописать «Enabled: false» в конец файла через пустую строку — значит
# создать вторую запись без Types; apt считает такой файл битым и отказывается
# читать вообще все источники. Поэтому поле кладётся внутрь записи.

# Файл, приведённый к виду «выключено»: служебные строки keel и любые Enabled
# убираются, опустевшие записи исчезают, а в конец каждой настоящей записи
# дописывается выключатель. Повторный прогон по своему же результату ничего
# не меняет.
_repos_deb822_disable() {
  awk '
    function flush(   i, has_types) {
      if (n == 0) return
      has_types = 0
      for (i = 1; i <= n; i++) if (buf[i] ~ /^[[:space:]]*[Tt]ypes:/) has_types = 1
      if (!first) print ""
      first = 0
      for (i = 1; i <= n; i++) print buf[i]
      if (has_types) { print "# Выключено keel"; print "Enabled: false" }
      n = 0
    }
    BEGIN { n = 0; first = 1 }
    /^[[:space:]]*$/                            { flush(); next }
    /^[[:space:]]*#[[:space:]]*Выключено keel[[:space:]]*$/ { next }
    /^[[:space:]]*[Ee]nabled:/                  { next }
    { buf[++n] = $0 }
    END { flush() }
  ' "$1"
}

# Выключен ли файл .sources: записи есть, у каждой есть Types и выключатель.
# Запись без Types — это наш старый брак, и он тоже считается «не выключено»,
# чтобы применение его перезаписало.
_repos_deb822_is_disabled() {
  awk '
    function close_stanza(   i, has_types, has_off, low) {
      if (n == 0) return
      has_types = 0; has_off = 0
      for (i = 1; i <= n; i++) {
        low = tolower(buf[i])
        if (low ~ /^[[:space:]]*types:/) has_types = 1
        if (low ~ /^[[:space:]]*enabled:[[:space:]]*(false|no|0)[[:space:]]*$/) has_off = 1
      }
      if (!has_types) bad = 1
      else { stanzas++; if (!has_off) bad = 1 }
      n = 0
    }
    BEGIN { n = 0; stanzas = 0; bad = 0 }
    /^[[:space:]]*$/ { close_stanza(); next }
    /^[[:space:]]*#/ { next }
    { buf[++n] = $0 }
    END { close_stanza(); exit (bad || stanzas == 0) ? 1 : 0 }
  ' "$1" 2>/dev/null
}

# Уже выключён?
_repos_is_disabled() {
  local f=$1
  if [[ "$f" == *.sources ]]; then
    _repos_deb822_is_disabled "$f"
  else
    ! grep -qE '^[[:space:]]*deb[[:space:]]' "$f" 2>/dev/null
  fi
}

# Содержимое того же файла, но выключенного
_repos_disabled_content() {
  local f=$1
  if [[ "$f" == *.sources ]]; then
    _repos_deb822_disable "$f"
  else
    sed 's/^[[:space:]]*deb[[:space:]]/# Выключено keel: &/' "$f"
  fi
}

_repos_file_matches() {
  local path=$1 want=$2
  [[ -f "$path" ]] || return 1
  diff -q <(printf '%s\n' "$want") "$path" >/dev/null 2>&1
}

# --- Контракт модуля ---------------------------------------------------------

mod_check() {
  local target; target=$(_repos_target)
  [[ -n "$target" ]] || return "$KEEL_RC_SKIP"

  case "$target" in
    no-subscription|enterprise|test) ;;
    *)
      err "host.repos = «${target}» — допустимо: no-subscription, enterprise, test"
      return 1
      ;;
  esac

  local changes=0 path want f
  path=$(_repos_file "$target")
  want=$(_repos_desired_content "$target")

  if ! _repos_file_matches "$path" "$want"; then
    printf 'включить репозиторий %s → %s\n' "$target" "$path"
    changes=1
  fi

  while IFS= read -r f; do
    [[ -n "$f" ]] || continue
    if ! _repos_is_disabled "$f"; then
      printf 'выключить: %s\n' "$f"
      changes=1
    fi
  done < <(_repos_other_files "$target")

  if (( changes )); then
    printf 'обновить список пакетов (apt update)\n'
    return "$KEEL_RC_CHANGES"
  fi
  return "$KEEL_RC_OK"
}

mod_apply() {
  local target; target=$(_repos_target)
  [[ -n "$target" ]] || return "$KEEL_RC_SKIP"

  local path want f
  path=$(_repos_file "$target")
  want=$(_repos_desired_content "$target")

  if ! _repos_file_matches "$path" "$want"; then
    printf '%s' "$want" | run_write "Включить репозиторий ${target}" "$path"
  fi

  while IFS= read -r f; do
    [[ -n "$f" ]] || continue
    _repos_is_disabled "$f" && continue
    _repos_disabled_content "$f" | run_write "Выключить репозиторий: ${f}" "$f"
  done < <(_repos_other_files "$target")

  # Настоящий apt трогаем только если работаем с настоящим корнем
  if [[ -z "$KEEL_FS_ROOT" ]]; then
    # Те же ограничители, что и в 20-updates: не ждать две минуты каждого
    # молчащего зеркала и не копить очередь на повторах
    run "Обновить список пакетов" apt-get update \
      -o Acquire::Retries=1 -o Acquire::http::Timeout=30
  fi
}

mod_verify() {
  local target; target=$(_repos_target)
  [[ -n "$target" ]] || return "$KEEL_RC_SKIP"

  local rc=0 path want f
  path=$(_repos_file "$target")
  want=$(_repos_desired_content "$target")

  if _repos_file_matches "$path" "$want"; then
    printf 'репозиторий %s включён\n' "$target"
  else
    printf 'репозиторий %s НЕ настроен (%s)\n' "$target" "$path"
    rc=$KEEL_RC_CHANGES
  fi

  while IFS= read -r f; do
    [[ -n "$f" ]] || continue
    if ! _repos_is_disabled "$f"; then
      printf 'всё ещё включён: %s\n' "$f"
      rc=$KEEL_RC_CHANGES
    fi
  done < <(_repos_other_files "$target")

  return "$rc"
}
