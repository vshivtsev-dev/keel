#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: интерфейс
#
# Обёртки над whiptail (он есть на любом Debian/Proxmox из коробки).
# Если whiptail недоступен или мы не в терминале — всё то же самое
# работает обычным текстом. Ни один модуль не вызывает whiptail напрямую.
#
# Граница, по которой здесь всё поделено: окна — для выбора, текст — для
# работы. Выбрать пункт меню или отметить гостей галочками удобнее стрелками,
# и окно там ничему не мешает — выбирать не из чего, кроме списка. А вот
# согласиться с планом нужно, глядя на сам план: окно newt очищает экран, и
# ровно то, ради чего человека спрашивают, исчезает у него из-под носа.
# Поэтому ui_confirm_plan окном не бывает никогда.

KEEL_UI="${KEEL_UI:-auto}"   # auto | plain

ui_has_whiptail() {
  [[ "$KEEL_UI" != "plain" ]] || return 1
  command -v whiptail >/dev/null 2>&1 || return 1
  [[ -t 2 ]] || return 1
  return 0
}

# Высота окна под количество строк текста, в разумных пределах
_ui_height() {
  local lines=$1 extra=${2:-8} h
  h=$(( lines + extra ))
  (( h < 12 )) && h=12
  (( h > 30 )) && h=30
  printf '%s' "$h"
}

_ui_lines() { printf '%s' "$(printf '%s' "$1" | wc -l)"; }

ui_msgbox() {
  local title=$1 text=$2
  if ui_has_whiptail; then
    whiptail --title "$title" --scrolltext \
      --msgbox "$text" "$(_ui_height "$(_ui_lines "$text")")" 78
  else
    printf '\n--- %s ---\n%s\n\n' "$title" "$text"
  fi
}

ui_yesno() {
  local title=$1 text=$2
  if ui_has_whiptail; then
    whiptail --title "$title" --yesno "$text" \
      "$(_ui_height "$(_ui_lines "$text")")" 78
  else
    local reply
    printf '\n--- %s ---\n%s\n' "$title" "$text" >&2
    read -r -p "Продолжить? [y/N] " reply </dev/tty
    [[ "$reply" == [yYдД]* ]]
  fi
}

# ui_menu "заголовок" "текст" тег1 "пункт 1" тег2 "пункт 2" ...
# Печатает выбранный тег в stdout. Пустой вывод = отмена.
ui_menu() {
  local title=$1 text=$2; shift 2
  local count=$(( $# / 2 ))
  if ui_has_whiptail; then
    # --default-item задаёт выделенный пункт явно, а не оставляет на усмотрение
    # newt. Флаг относится только к --menu; у --checklist его нет, и пробовать
    # там не будем: whiptail на неизвестный флаг просто выходит с ошибкой, а мы
    # её глушим — получился бы молча пустой выбор.
    whiptail --title "$title" --notags --default-item "$1" --menu "$text" \
      "$(_ui_height "$count" 10)" 78 "$count" "$@" 3>&1 1>&2 2>&3 || true
  else
    local i=1 tags=() args=("$@")
    printf '\n--- %s ---\n%s\n' "$title" "$text" >&2
    while (( ${#args[@]} )); do
      tags+=("${args[0]}")
      printf '  %2d) %s\n' "$i" "${args[1]}" >&2
      args=("${args[@]:2}"); i=$(( i + 1 ))
    done
    local reply
    read -r -p "Номер пункта (пусто — отмена): " reply </dev/tty
    [[ "$reply" =~ ^[0-9]+$ ]] || return 0
    (( reply >= 1 && reply <= ${#tags[@]} )) || return 0
    printf '%s' "${tags[$(( reply - 1 ))]}"
  fi
}

# ui_checklist "заголовок" "текст" тег1 "пункт" on/off ...
# Печатает выбранные теги через пробел.
ui_checklist() {
  local title=$1 text=$2; shift 2
  local count=$(( $# / 3 ))
  if ui_has_whiptail; then
    whiptail --title "$title" --notags --checklist "$text" \
      "$(_ui_height "$count" 10)" 78 "$count" "$@" 3>&1 1>&2 2>&3 || true
  else
    local i=1 tags=() args=("$@")
    printf '\n--- %s ---\n%s\n' "$title" "$text" >&2
    while (( ${#args[@]} )); do
      tags+=("${args[0]}")
      printf '  %2d) %s\n' "$i" "${args[1]}" >&2
      args=("${args[@]:3}"); i=$(( i + 1 ))
    done
    local reply out=()
    read -r -p "Номера через пробел (пусто — ничего): " reply </dev/tty
    local n
    for n in $reply; do
      [[ "$n" =~ ^[0-9]+$ ]] || continue
      (( n >= 1 && n <= ${#tags[@]} )) && out+=("\"${tags[$(( n - 1 ))]}\"")
    done
    printf '%s' "${out[*]}"
  fi
}

# Согласие на весь план, одним вопросом. Печатает в stdout ровно одно слово:
# apply | detail | step | cancel.
#
# Окна здесь нет намеренно — см. заголовок файла. План только что проехал по
# экрану, и вопрос должен стоять под ним, а не поверх него.
#
# Ответов четыре, потому что «да/нет» на список из полусотни команд — выбор
# между доверием и отказом, а нужен третий путь: «покажи подробно» разворачивает
# план до каждой команды и каждого diff, «по шагам» возвращает старое поведение
# для тех случаев, когда хочется держать руку на рубильнике.
ui_confirm_plan() {
  local prompt=${1:-"Применить эти изменения?"} reply

  # Подсказки — строго в stderr: stdout здесь это ответ, и note() посреди него
  # превратил бы «cancel» в строку, которую вызывающая сторона не узнает
  if ! keel_have_tty; then
    err "Некому задать вопрос: терминала нет."
    note "Для неинтерактивного запуска есть keel apply --yes (после keel plan)." >&2
    printf 'cancel'
    return 0
  fi

  printf '\n%s%s%s [y/N]  %sd — подробно, s — по шагам%s\n' \
    "$C_BOLD" "$prompt" "$C_RESET" "$C_DIM" "$C_RESET" >&2
  read -r -p "> " reply </dev/tty || reply=""

  # Русские буквы здесь не для красоты: на консоли Proxmox легко забыть
  # переключить раскладку, и тогда клавиша d даёт «в», а s — «ы». Плюс «д» и
  # «да» как обычное согласие — так же, как их принимает ui_yesno.
  # «н» намеренно не принимается: это и «y» вслепую, и начало слова «нет».
  case "$reply" in
    y|Y|д|Д|yes|да) printf 'apply' ;;
    d|D|в|В)        printf 'detail' ;;
    s|S|ы|Ы)        printf 'step' ;;
    *)              printf 'cancel' ;;
  esac
}

# Экран подтверждения одного изменения.
# Печатает в stdout ровно одно слово: apply | skip | abort
#
# Здесь нет --scrolltext, и это важно. В newt прокручиваемый текстовый блок
# принимает фокус и стоит в форме первым: при открытии стрелки листали бы
# diff, а до списка пришлось бы уходить вправо. Поэтому текст обрезается до
# экрана, а целиком его показывает отдельный пункт меню — там прокрутка
# уместна, кнопка одна, и фокус на тексте это ровно то, что нужно.
KEEL_CONFIRM_BODY_LINES="${KEEL_CONFIRM_BODY_LINES:-14}"

_ui_clip() {
  local body=$1 limit=$2 n
  n=$(printf '%s\n' "$body" | wc -l)
  if (( n <= limit )); then
    printf '%s' "$body"
    return 0
  fi
  printf '%s\n' "$body" | head -n "$limit"
  printf '… ещё %s строк — пункт «показать целиком»' "$(( n - limit ))"
}

ui_confirm_step() {
  local desc=$1 body=$2
  if ui_has_whiptail; then
    local short text choice
    short=$(_ui_clip "$body" "$KEEL_CONFIRM_BODY_LINES")
    text="Будет выполнено:

${desc}

${short}"
    while true; do
      choice=$(whiptail --title "Подтверждение изменения" --notags \
        --default-item apply --menu "$text" \
        "$(_ui_height "$(_ui_lines "$text")" 12)" 78 4 \
        apply "Применить" \
        skip  "Пропустить этот шаг" \
        full  "Показать целиком" \
        abort "Прервать" 3>&1 1>&2 2>&3) || choice="abort"
      if [[ "$choice" == "full" ]]; then
        ui_msgbox "$desc" "$body"
        continue
      fi
      printf '%s' "${choice:-abort}"
      return 0
    done
  else
    # Без терминала спрашивать не у кого — и молча применять тем более нельзя.
    # Пустой ответ здесь значит «применить», а неоткрытый /dev/tty оставляет
    # ответ ровно пустым: без этой проверки пошаговый режим в неинтерактивном
    # запуске применял бы всё подряд, не спросив ни разу.
    if ! keel_have_tty; then
      err "Нужен ответ на каждый шаг, но терминала нет."
      note "Для неинтерактивного запуска: keel apply --yes (после keel plan)." >&2
      printf 'abort'
      return 0
    fi
    printf '\n%s\n%s\n' "$desc" "$body" >&2
    local reply
    read -r -p "Enter — применить, s — пропустить, любое другое — прервать: " reply </dev/tty
    case "$reply" in
      ""|y|Y|p|P) printf 'apply' ;;
      s|S)        printf 'skip' ;;
      *)          printf 'abort' ;;
    esac
  fi
}

# Запрос пароля. В текстовом режиме ввод не отображается.
# Пустой ответ — вызывающая сторона решает, что делать.
ui_password() {
  local title=$1 text=$2
  if ui_has_whiptail; then
    whiptail --title "$title" --passwordbox "$text" 12 70 3>&1 1>&2 2>&3 || true
  else
    local reply
    printf '\n--- %s ---\n%s\n' "$title" "$text" >&2
    read -r -s -p "Пароль (пусто — сгенерировать): " reply </dev/tty
    printf '\n' >&2
    printf '%s' "$reply"
  fi
}

# Однострочный ввод. Печатает введённое в stdout, пустое — отмена.
ui_input() {
  local title=$1 text=$2 default=${3:-}
  if ui_has_whiptail; then
    whiptail --title "$title" --inputbox "$text" 12 78 "$default" 3>&1 1>&2 2>&3 || true
  else
    local reply
    printf '\n--- %s ---\n%s\n' "$title" "$text" >&2
    read -r -p "> " -e -i "$default" reply </dev/tty || true
    printf '%s' "$reply"
  fi
}
