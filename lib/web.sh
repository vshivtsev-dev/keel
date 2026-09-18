#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: запуск веб-интерфейса
#
# Тонкая обёртка: завести токен, сказать адрес и отдать управление
# lib/web.pl. Вся работа с HTTP там, здесь только то, что удобнее на bash, —
# секреты и проверки окружения.
#
# Веб-интерфейс — надстройка, а не замена. Всё, что он умеет, умеет и
# терминал; обратное неверно: пока Proxmox не поднялся, поднимать HTTP-сервер
# негде, и восстановление идёт из консоли.

KEEL_WEB_LISTEN="${KEEL_WEB_LISTEN:-127.0.0.1}"
KEEL_WEB_PORT="${KEEL_WEB_PORT:-8777}"

# Токен переживает перезапуск: иначе открытая вкладка умирала бы вместе с
# сервером, а адрес приходилось бы переносить руками при каждом старте.
_web_token() {
  local file="${KEEL_SECRETS_DIR}/web.txt"
  if [[ -s "$file" ]]; then
    tr -d '[:space:]' <"$file"
    return 0
  fi
  local tok
  tok=$(head -c 24 /dev/urandom | base64 | tr -d '/+=' | head -c 32)
  mkdir -p "$KEEL_SECRETS_DIR"          # keel:allow-direct каталог самого keel
  printf '%s\n' "$tok" >"$file"
  chmod 600 "$file"                     # keel:allow-direct файл самого keel
  printf '%s' "$tok"
}

# Адрес, по которому эту страницу реально открыть с другой машины.
# hostname -I отдаёт все адреса хоста; берём первый — он почти всегда тот,
# по которому к хосту и ходят.
_web_lan_addr() {
  local ip
  ip=$(hostname -I 2>/dev/null | awk '{print $1}')
  printf '%s' "${ip:-АДРЕС-ХОСТА}"
}

web_command() {
  # Адрес и порт приходят флагами --listen и --port, их разбирает parse_args
  [[ "$KEEL_WEB_PORT" =~ ^[0-9]+$ ]] || die "Порт должен быть числом, а не «${KEEL_WEB_PORT}»."
  # Ноль означает «любой свободный», и его выбирает ядро уже внутри сервера —
  # то есть адрес, который здесь печатается, оказался бы неверным. Показывать
  # человеку нерабочую ссылку хуже, чем попросить назвать порт.
  (( KEEL_WEB_PORT >= 1 && KEEL_WEB_PORT <= 65535 )) \
    || die "Порт ${KEEL_WEB_PORT} вне диапазона 1–65535."

  # Страница показывает план и применяет его — то есть делает всё то же, что
  # keel apply. Значит и права нужны те же.
  need_root

  perl -MIO::Socket::INET -MJSON::PP -MDigest::SHA -e 'exit 0' >/dev/null 2>&1 \
    || die "Нет perl с IO::Socket::INET, JSON::PP и Digest::SHA. Это странно для Proxmox."

  local token url
  token=$(_web_token)

  if [[ "$KEEL_WEB_LISTEN" == "127.0.0.1" || "$KEEL_WEB_LISTEN" == "localhost" ]]; then
    url="http://127.0.0.1:${KEEL_WEB_PORT}/?t=${token}"
  else
    url="http://$(_web_lan_addr):${KEEL_WEB_PORT}/?t=${token}"
  fi

  head1 "keel web"
  info "Адрес: ${url}"
  note "Токен лежит в ${KEEL_SECRETS_DIR}/web.txt — тот же адрес можно собрать заново."

  if [[ "$KEEL_WEB_LISTEN" == "127.0.0.1" || "$KEEL_WEB_LISTEN" == "localhost" ]]; then
    note "Слушаю только этот хост. С ноутбука — через туннель:"
    note "  ssh -N -L ${KEEL_WEB_PORT}:127.0.0.1:${KEEL_WEB_PORT} root@$(_web_lan_addr)"
  else
    warn "Слушаю ${KEEL_WEB_LISTEN} — страница доступна всем, кто дотянется до этого порта."
    warn "Она работает под root и меняет хост. В общей сети так делать не стоит:"
    warn "оставь 127.0.0.1 и пробрось порт по ssh."
  fi

  note "Остановить — Ctrl+C."
  printf '\n'

  export KEEL_MANIFEST KEEL_VERSION
  # exec, а не фон: сервером управляет тот же терминал, Ctrl+C останавливает
  # его, и не появляется процесса, о котором никто не помнит
  exec perl "${KEEL_ROOT}/lib/web.pl" \
    --listen "$KEEL_WEB_LISTEN" \
    --port   "$KEEL_WEB_PORT" \
    --token  "$token" \
    --root   "$KEEL_ROOT" \
    --home   "$KEEL_HOME"
}
