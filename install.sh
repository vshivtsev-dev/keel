#!/usr/bin/env bash
#
# keel :: установка на хост Proxmox VE.
#
#   bash -c "$(curl -fsSL https://ТВОЙ-ДОМЕН/install.sh)"
#
# Ставит один статический бинарник и раскладывает каталог /root/keel:
# манифест, секреты, копии файлов, логи, планы. Систему при этом не
# настраивает — это делает уже сам keel, и только после того, как ты
# посмотришь план.
#
# Зависимостей у keel нет никаких: ни perl, ни whiptail, ни даже
# совместимой libc. Инструмент восстановления, который перед работой
# просит что-то доустановить, — плохой инструмент восстановления.
#
# Скачанный бинарник сверяется с контрольной суммой. От тебя для этого
# ничего не требуется: сумма едет вместе с ним. Раньше по этой ссылке
# приезжал читаемый скрипт, который можно было открыть и посмотреть;
# теперь это непрозрачный файл, и сверка — единственное, что остаётся.

set -Eeuo pipefail

KEEL_DIST="${KEEL_DIST:-https://keel.example.invalid}"
KEEL_HOME="${KEEL_HOME:-/root/keel}"
KEEL_BIN="${KEEL_BIN:-/usr/local/bin/keel}"

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
elif [[ "${KEEL_ASSUME_YES:-}" == "1" ]]; then
  warn "Это не похоже на хост Proxmox VE — ставлю, потому что задан KEEL_ASSUME_YES=1."
else
  warn "Это не похоже на хост Proxmox VE. keel рассчитан на него."
  # Спрашиваем у терминала напрямую: установщик запускают через
  # «bash -c "$(curl …)"», и на обычном stdin в этот момент висит сам
  # скрипт. Терминала нет вовсе — значит спросить некого, и ставить
  # молча на непонятную машину не стоит.
  if [[ ! -r /dev/tty ]]; then
    die "Спросить некого: терминала нет. Если уверен — KEEL_ASSUME_YES=1 перед командой."
  fi
  read -r -p "Продолжить всё равно? [y/N] " reply </dev/tty || die "Отменено."
  [[ "$reply" == [yY] ]] || die "Отменено."
fi

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "Неизвестная архитектура $(uname -m) — сборки под неё нет." ;;
esac

command -v sha256sum >/dev/null 2>&1 \
  || die "Нет sha256sum — сверить скачанное нечем. Это странно для Debian."

fetch() {
  local url=$1 dest=$2
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --retry-delay 2 -o "$dest" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --tries=3 -O "$dest" "$url"
  else
    die "Нет ни curl, ни wget — скачать нечем."
  fi
}

# --- Переезд со старой раскладки ---------------------------------------------
#
# Версии до 0.2.0 раскладывали keel по трём местам: код в /opt/keel, данные
# в /var/lib/keel, логи в /var/log/keel. Помнить три пути неудобно, поэтому
# всё переезжает в один каталог. Данные переносятся, код — нет: удалить
# старый каталог с кодом решает человек.

migrate_dir() {
  local from=$1 to=$2 what=$3
  [[ -d "$from" ]] || return 0
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

NAME="keel-linux-${ARCH}"
tmp=$(mktemp -d)
# shellcheck disable=SC2064  # путь подставляется сейчас, это и нужно
trap "rm -rf '$tmp'" EXIT

printf 'Скачиваю %s/%s\n' "$KEEL_DIST" "$NAME"
fetch "${KEEL_DIST}/${NAME}"       "${tmp}/${NAME}"     || die "Не удалось скачать ${NAME}"
fetch "${KEEL_DIST}/checksums.txt" "${tmp}/checksums.txt" || die "Не удалось скачать контрольные суммы"

# Сверяем молча и по-настоящему: берём из общего файла строку про наш файл
# и проверяем её штатным sha256sum, а не сравнением глазами.
want=$(awk -v n="$NAME" '$2 == n { print $1 }' "${tmp}/checksums.txt" | head -n1)
[[ -n "$want" ]] || die "В checksums.txt нет строки про ${NAME} — раздача собрана неправильно."

got=$(sha256sum "${tmp}/${NAME}" | awk '{print $1}')
if [[ "$got" != "$want" ]]; then
  printf '  ожидалась: %s\n  получена:  %s\n' "$want" "$got" >&2
  die "Контрольная сумма не сошлась. Не ставлю: скачалось не то, что собиралось."
fi
ok "Контрольная сумма сошлась"

install -m 0755 "${tmp}/${NAME}" "$KEEL_BIN"
ok "Команда keel доступна: ${KEEL_BIN}"

# Каталог и манифест раскладывает сам keel: он знает свою раскладку лучше,
# чем установщик, и примером манифеста тоже владеет он.
"$KEEL_BIN" init

if [[ -d /opt/keel ]]; then
  warn "Старый каталог кода /opt/keel больше не используется. Убрать: rm -rf /opt/keel"
fi

cat <<EOF

Готово: $("$KEEL_BIN" version)

Дальше по порядку:

  keel doctor     посмотреть, что за хост и что с видеокартой
  keel plan       увидеть, что изменится — ничего не выполняется
  keel            открыть экран

Обновить потом: keel update
EOF
