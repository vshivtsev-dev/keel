#!/usr/bin/env bash
#
# keel :: установка на хост Proxmox VE.
#
#   bash -c "$(curl -fsSL https://raw.githubusercontent.com/vshivtsev-dev/proxmox/main/install.sh)"
#
# Скрипт кладёт репозиторий в /opt/keel и делает команду keel доступной.
# Ничего в системе он не настраивает — это делает уже сам keel, и только
# после того, как ты посмотришь план.
#
# git не требуется: на свежем Proxmox его нет, а инструмент восстановления,
# который перед работой просит что-то доустановить, — плохой инструмент
# восстановления. Если git всё же есть, используется он: тогда обновляться
# можно через git pull.

set -Eeuo pipefail

KEEL_OWNER="${KEEL_OWNER:-vshivtsev-dev}"
KEEL_NAME="${KEEL_NAME:-proxmox}"
KEEL_BRANCH="${KEEL_BRANCH:-main}"
KEEL_PREFIX="${KEEL_PREFIX:-/opt/keel}"
KEEL_BIN="${KEEL_BIN:-/usr/local/bin/keel}"

KEEL_REPO="${KEEL_REPO:-https://github.com/${KEEL_OWNER}/${KEEL_NAME}.git}"

# Токен нужен, только если репозиторий закрытый. Создать:
# GitHub → Settings → Developer settings → Personal access tokens,
# права: Contents — read.
KEEL_TOKEN="${KEEL_TOKEN:-}"

if [[ -n "$KEEL_TOKEN" ]]; then
  # У закрытого репозитория архив отдаёт только API, и только по токену
  KEEL_TARBALL="${KEEL_TARBALL:-https://api.github.com/repos/${KEEL_OWNER}/${KEEL_NAME}/tarball/${KEEL_BRANCH}}"
else
  KEEL_TARBALL="${KEEL_TARBALL:-https://codeload.github.com/${KEEL_OWNER}/${KEEL_NAME}/tar.gz/refs/heads/${KEEL_BRANCH}}"
fi

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

# Скачать по ссылке в файл: curl или wget, что есть
download() {
  local url=$1 dest=$2
  if command -v curl >/dev/null 2>&1; then
    if [[ -n "$KEEL_TOKEN" ]]; then
      curl -fsSL -H "Authorization: Bearer ${KEEL_TOKEN}" -o "$dest" "$url"
    else
      curl -fsSL -o "$dest" "$url"
    fi
  elif command -v wget >/dev/null 2>&1; then
    if [[ -n "$KEEL_TOKEN" ]]; then
      wget -q --header="Authorization: Bearer ${KEEL_TOKEN}" -O "$dest" "$url"
    else
      wget -qO "$dest" "$url"
    fi
  else
    die "Нет ни curl, ни wget — скачать репозиторий нечем."
  fi
}

install_from_tarball() {
  command -v tar >/dev/null 2>&1 || die "Нет tar — распаковать репозиторий нечем."
  local tmp; tmp=$(mktemp)
  # shellcheck disable=SC2064  # путь подставляется сейчас, это и нужно
  trap "rm -f '$tmp'" RETURN

  printf 'Скачиваю ветку %s\n' "$KEEL_BRANCH"
  if ! download "$KEEL_TARBALL" "$tmp"; then
    printf '\n' >&2
    warn "Не удалось скачать ${KEEL_TARBALL}"
    if [[ -z "$KEEL_TOKEN" ]]; then
      warn "Если репозиторий закрытый, нужен токен:"
      warn "  KEEL_TOKEN=ghp_... bash install.sh"
      warn "Либо перенеси каталог на хост руками — см. docs/TESTING.md, шаг 1."
    fi
    die "Установка прервана."
  fi

  mkdir -p "$KEEL_PREFIX"
  # Твой manifest/host.json в архиве отсутствует (он в .gitignore),
  # поэтому распаковка поверх его не затирает
  tar -xzf "$tmp" -C "$KEEL_PREFIX" --strip-components=1 \
    || die "Не удалось распаковать архив в ${KEEL_PREFIX}"
  ok "Репозиторий распакован в ${KEEL_PREFIX}"
  warn "Установлено без git: обновляться — повторным запуском install.sh."
}

install_from_git() {
  if [[ -d "${KEEL_PREFIX}/.git" ]]; then
    ok "keel уже установлен в ${KEEL_PREFIX}, обновляю"
    git -C "$KEEL_PREFIX" fetch --quiet origin "$KEEL_BRANCH"
    git -C "$KEEL_PREFIX" checkout --quiet "$KEEL_BRANCH"
    git -C "$KEEL_PREFIX" pull --quiet --ff-only origin "$KEEL_BRANCH"
  else
    git clone --quiet --branch "$KEEL_BRANCH" "$KEEL_REPO" "$KEEL_PREFIX"
    ok "Репозиторий склонирован в ${KEEL_PREFIX}"
  fi
}

# --- Поехали -----------------------------------------------------------------

[[ "$(id -u)" -eq 0 ]] || die "Нужны права root: запусти под root или через sudo."

if [[ -f /etc/pve/.version ]] || command -v pveversion >/dev/null 2>&1; then
  ok "Proxmox VE обнаружен: $(pveversion 2>/dev/null | head -n1)"
else
  warn "Это не похоже на хост Proxmox VE."
  ask "Продолжить всё равно?" || die "Отменено."
fi

# whiptail нужен только для меню: без него keel работает текстом
if ! command -v whiptail >/dev/null 2>&1; then
  warn "Нет whiptail — меню будет текстовым (поставить: apt install whiptail)."
fi
perl -MJSON::PP -e 'exit 0' >/dev/null 2>&1 \
  || die "Нет perl с JSON::PP — читать манифест нечем. Это странно для Proxmox."

if [[ -e "$KEEL_PREFIX" && ! -d "$KEEL_PREFIX" ]]; then
  die "${KEEL_PREFIX} существует и это не каталог. Убери его или задай KEEL_PREFIX."
fi

if command -v git >/dev/null 2>&1; then
  install_from_git
else
  install_from_tarball
fi

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
