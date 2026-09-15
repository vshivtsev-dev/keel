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

# Уже выключён?
_repos_is_disabled() {
  local f=$1
  if [[ "$f" == *.sources ]]; then
    grep -qi '^Enabled:[[:space:]]*false' "$f" 2>/dev/null
  else
    ! grep -qE '^[[:space:]]*deb[[:space:]]' "$f" 2>/dev/null
  fi
}

# Содержимое того же файла, но выключенного
_repos_disabled_content() {
  local f=$1
  if [[ "$f" == *.sources ]]; then
    if grep -qi '^Enabled:' "$f" 2>/dev/null; then
      sed 's/^[Ee]nabled:.*/Enabled: false/' "$f"
    else
      cat "$f"
      printf '\n# Выключено keel\nEnabled: false\n'
    fi
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
    run "Обновить список пакетов" apt-get update
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
