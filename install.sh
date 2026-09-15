#!/usr/bin/env bash
#
# keel :: установка на хост Proxmox VE.
#
#   bash -c "$(curl -fsSL https://raw.githubusercontent.com/vshivtsev-dev/proxmox/main/install.sh)"
#
# Скрипт кладёт репозиторий в /opt/keel и делает команду keel доступной.
# Ничего в системе он не настраивает — это делает уже сам keel, и только
# после того, как ты посмотришь план.

set -Eeuo pipefail

KEEL_REPO="${KEEL_REPO:-https://github.com/vshivtsev-dev/proxmox.git}"
KEEL_BRANCH="${KEEL_BRANCH:-main}"
KEEL_PREFIX="${KEEL_PREFIX:-/opt/keel}"
KEEL_BIN="${KEEL_BIN:-/usr/local/bin/keel}"

if [[ -t 1 ]]; then
  C_RESET=$'\033[0m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_RED=$'\033[31m'
else
  C_RESET=''; C_GREEN=''; C_YELLOW=''; C_RED=''
fi
ok()   { printf '%s✓%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn() { printf '%s!%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
die()  { printf '%s✗%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; exit 1; }

ask() {
  local prompt=$1 reply
  read -r -p "${prompt} [y/N] " reply </dev/tty || return 1
  [[ "$reply" == [yY] ]]
}

[[ "$(id -u)" -eq 0 ]] || die "Нужны права root: запусти под root или через sudo."

if [[ -f /etc/pve/.version ]] || command -v pveversion >/dev/null 2>&1; then
  ok "Proxmox VE обнаружен: $(pveversion 2>/dev/null | head -n1)"
else
  warn "Это не похоже на хост Proxmox VE."
  ask "Продолжить всё равно?" || die "Отменено."
fi

# Зависимости. git нужен для установки, whiptail — для меню.
missing=()
command -v git      >/dev/null 2>&1 || missing+=(git)
command -v whiptail >/dev/null 2>&1 || missing+=(whiptail)
perl -MJSON::PP -e 'exit 0' >/dev/null 2>&1 || missing+=(perl)

if (( ${#missing[@]} )); then
  warn "Не хватает: ${missing[*]}"
  if ask "Установить их через apt?"; then
    apt-get update
    apt-get install -y "${missing[@]}"
  else
    die "Без них установка не имеет смысла."
  fi
fi

if [[ -d "${KEEL_PREFIX}/.git" ]]; then
  ok "keel уже установлен в ${KEEL_PREFIX}, обновляю"
  git -C "$KEEL_PREFIX" fetch --quiet origin "$KEEL_BRANCH"
  git -C "$KEEL_PREFIX" checkout --quiet "$KEEL_BRANCH"
  git -C "$KEEL_PREFIX" pull --quiet --ff-only origin "$KEEL_BRANCH"
elif [[ -e "$KEEL_PREFIX" ]]; then
  die "${KEEL_PREFIX} уже существует и это не репозиторий keel. Убери его или задай KEEL_PREFIX."
else
  git clone --quiet --branch "$KEEL_BRANCH" "$KEEL_REPO" "$KEEL_PREFIX"
  ok "Репозиторий склонирован в ${KEEL_PREFIX}"
fi

chmod +x "${KEEL_PREFIX}/bin/keel"
ln -sfn "${KEEL_PREFIX}/bin/keel" "$KEEL_BIN"
ok "Команда keel доступна: ${KEEL_BIN}"

if [[ ! -f "${KEEL_PREFIX}/manifest/host.json" ]]; then
  cp "${KEEL_PREFIX}/manifest/host.example.json" "${KEEL_PREFIX}/manifest/host.json"
  ok "Создан манифест ${KEEL_PREFIX}/manifest/host.json — поправь его под себя"
fi

cat <<EOF

Готово. Дальше по порядку:

  keel doctor     посмотреть, что за хост и что с видеокартой
  keel plan       увидеть, что изменится — ничего не выполняется
  keel            меню

Манифест (единственный файл, который ты правишь):
  ${KEEL_PREFIX}/manifest/host.json
EOF
