#!/usr/bin/env bash
#
# keel :: установка на хост Proxmox VE.
#
#   bash -c "$(curl -fsSL https://raw.githubusercontent.com/vshivtsev-dev/keel/main/install.sh)"
#
# Кладёт репозиторий в /opt/keel и делает команду keel доступной.
# Систему при этом не настраивает — это делает уже сам keel, и только
# после того, как ты посмотришь план.
#
# git не нужен: на свежем Proxmox его нет, а инструмент восстановления,
# который перед работой просит что-то доустановить, — плохой инструмент
# восстановления. Обновление — повторный запуск этой же команды.

set -Eeuo pipefail

KEEL_OWNER="${KEEL_OWNER:-vshivtsev-dev}"
KEEL_NAME="${KEEL_NAME:-keel}"
KEEL_BRANCH="${KEEL_BRANCH:-main}"
KEEL_PREFIX="${KEEL_PREFIX:-/opt/keel}"
KEEL_BIN="${KEEL_BIN:-/usr/local/bin/keel}"
KEEL_TARBALL="${KEEL_TARBALL:-https://codeload.github.com/${KEEL_OWNER}/${KEEL_NAME}/tar.gz/refs/heads/${KEEL_BRANCH}}"

if [[ -t 1 ]]; then
  C_RESET=$'\033[0m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_RED=$'\033[31m'
else
  C_RESET=''; C_GREEN=''; C_YELLOW=''; C_RED=''
fi
ok()   { printf '%s✓%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn() { printf '%s!%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
die()  { printf '%s✗%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; exit 1; }

# --- Проверки окружения ------------------------------------------------------

[[ "$(id -u)" -eq 0 ]] || die "Нужны права root: запусти под root или через sudo."

if [[ -f /etc/pve/.version ]] || command -v pveversion >/dev/null 2>&1; then
  ok "Proxmox VE обнаружен: $(pveversion 2>/dev/null | head -n1)"
else
  warn "Это не похоже на хост Proxmox VE. keel рассчитан на него."
  read -r -p "Продолжить всё равно? [y/N] " reply </dev/tty || die "Отменено."
  [[ "$reply" == [yY] ]] || die "Отменено."
fi

perl -MJSON::PP -e 'exit 0' >/dev/null 2>&1 \
  || die "Нет perl с JSON::PP — читать манифест нечем. Это странно для Proxmox."
command -v tar >/dev/null 2>&1 || die "Нет tar — распаковать репозиторий нечем."

# whiptail нужен только для меню: без него keel работает текстом
command -v whiptail >/dev/null 2>&1 \
  || warn "Нет whiptail — меню будет текстовым (поставить: apt install whiptail)."

[[ -e "$KEEL_PREFIX" && ! -d "$KEEL_PREFIX" ]] \
  && die "${KEEL_PREFIX} существует и это не каталог. Убери его или задай KEEL_PREFIX."

# --- Установка ---------------------------------------------------------------

tmp=$(mktemp)
# shellcheck disable=SC2064  # путь подставляется сейчас, это и нужно
trap "rm -f '$tmp'" EXIT

printf 'Скачиваю %s\n' "$KEEL_TARBALL"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL -o "$tmp" "$KEEL_TARBALL" || die "Не удалось скачать ${KEEL_TARBALL}"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "$tmp" "$KEEL_TARBALL" || die "Не удалось скачать ${KEEL_TARBALL}"
else
  die "Нет ни curl, ни wget — скачать репозиторий нечем."
fi

mkdir -p "$KEEL_PREFIX"
# Твой manifest/host.json в архиве отсутствует (он в .gitignore),
# поэтому распаковка поверх его не затирает
tar -xzf "$tmp" -C "$KEEL_PREFIX" --strip-components=1 \
  || die "Не удалось распаковать архив в ${KEEL_PREFIX}"
ok "keel распакован в ${KEEL_PREFIX}"

chmod +x "${KEEL_PREFIX}/bin/keel" "${KEEL_PREFIX}/tests/"*.sh 2>/dev/null || true
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
