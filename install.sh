#!/usr/bin/env bash
#
# keel :: установка на хост Proxmox VE.
#
#   bash -c "$(curl -fsSL https://raw.githubusercontent.com/vshivtsev-dev/keel/main/install.sh)"
#
# Кладёт код в /root/keel/app и делает команду keel доступной. Всё, с чем
# работаешь ты — манифест, пароли, логи, копии, — лежит рядом, в /root/keel.
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
KEEL_HOME="${KEEL_HOME:-/root/keel}"
KEEL_PREFIX="${KEEL_PREFIX:-${KEEL_HOME}/app}"
KEEL_BIN="${KEEL_BIN:-/usr/local/bin/keel}"
KEEL_TARBALL="${KEEL_TARBALL:-https://codeload.github.com/${KEEL_OWNER}/${KEEL_NAME}/tar.gz/refs/heads/${KEEL_BRANCH}}"

if [[ -t 1 ]]; then
  C_RESET=$'\033[0m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_RED=$'\033[31m'
  C_DIM=$'\033[2m'
else
  C_RESET=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_DIM=''
fi
ok()   { printf '%s✓%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn() { printf '%s!%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
note() { printf '%s  %s%s\n' "$C_DIM" "$*" "$C_RESET"; }
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

# whiptail рисует только меню и списки с галочками. План, подтверждение и
# применение текстовые в любом случае, поэтому это заметка, а не дефект.
command -v whiptail >/dev/null 2>&1 \
  || note "Нет whiptail — меню будет списком с номерами (поставить: apt install whiptail)."

[[ -e "$KEEL_PREFIX" && ! -d "$KEEL_PREFIX" ]] \
  && die "${KEEL_PREFIX} существует и это не каталог. Убери его или задай KEEL_PREFIX."

# --- Переезд со старой раскладки ---------------------------------------------
#
# Версии до 0.2.0 раскладывали keel по трём местам: код в /opt/keel, данные в
# /var/lib/keel, логи в /var/log/keel. Помнить три пути неудобно, поэтому всё
# переезжает в один каталог. Данные переносятся, код — нет: удалить старый
# каталог с кодом решает человек.

migrate_dir() {
  local from=$1 to=$2 what=$3
  [[ -d "$from" ]] || return 0
  # Пустой каталог переносить незачем
  [[ -n "$(ls -A "$from" 2>/dev/null)" ]] || { rmdir "$from" 2>/dev/null || true; return 0; }
  mkdir -p "$to"
  local item
  for item in "$from"/* "$from"/.[!.]*; do
    [[ -e "$item" ]] || continue
    if [[ -e "${to}/$(basename "$item")" ]]; then
      warn "Уже есть ${to}/$(basename "$item") — оставляю как есть, старое в ${from}"
      continue
    fi
    mv "$item" "$to"/
  done
  rmdir "$from" 2>/dev/null || true
  ok "Перенесено: ${what} → ${to}"
}

if [[ -d /var/lib/keel || -d /var/log/keel || -f /opt/keel/manifest/host.json ]]; then
  printf '\nНашлась установка старой раскладки — переношу данные в %s\n' "$KEEL_HOME"
  mkdir -p "$KEEL_HOME"
  if [[ -f /opt/keel/manifest/host.json ]]; then
    if [[ -e "${KEEL_HOME}/host.json" ]]; then
      warn "Манифест ${KEEL_HOME}/host.json уже есть — старый оставлен в /opt/keel/manifest/"
    else
      mv /opt/keel/manifest/host.json "${KEEL_HOME}/host.json"
      ok "Перенесено: манифест → ${KEEL_HOME}/host.json"
    fi
  fi
  migrate_dir /var/lib/keel "$KEEL_HOME"        "состояние, пароли, копии, образы"
  migrate_dir /var/log/keel "${KEEL_HOME}/logs" "логи"
fi

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

mkdir -p "$KEEL_HOME" "$KEEL_PREFIX"
# Распаковка затирает app/ целиком — и это нормально: там только код.
# Твой host.json лежит уровнем выше, до него она не дотягивается.
tar -xzf "$tmp" -C "$KEEL_PREFIX" --strip-components=1 \
  || die "Не удалось распаковать архив в ${KEEL_PREFIX}"
ok "keel распакован в ${KEEL_PREFIX}"

chmod +x "${KEEL_PREFIX}/bin/keel" "${KEEL_PREFIX}/tests/"*.sh 2>/dev/null || true
ln -sfn "${KEEL_PREFIX}/bin/keel" "$KEEL_BIN"
ok "Команда keel доступна: ${KEEL_BIN}"

if [[ ! -f "${KEEL_HOME}/host.json" ]]; then
  cp "${KEEL_PREFIX}/manifest/host.example.json" "${KEEL_HOME}/host.json"
  ok "Создан манифест ${KEEL_HOME}/host.json — поправь его под себя"
fi

if [[ -d /opt/keel ]]; then
  warn "Старый каталог кода /opt/keel больше не используется. Убрать: rm -rf /opt/keel"
fi

cat <<EOF

Готово. Дальше по порядку:

  keel doctor     посмотреть, что за хост и что с видеокартой
  keel plan       увидеть, что изменится — ничего не выполняется
  keel            меню

Всё твоё — в одном каталоге ${KEEL_HOME}:

  host.json     манифест: единственный файл, который ты правишь
  secrets/      пароли гостей и токены
  backups/      копии файлов до того, как keel их правил
  logs/         логи запусков
  app/          код keel — перезаписывается при обновлении
EOF
