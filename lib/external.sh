#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: внешние инструменты
#
# Каталог community-scripts — сотни готовых установщиков приложений в LXC.
# Втаскивать их в keel незачем: у них своя жизнь и свой темп релизов.
# Но и делать вид, что их нет, глупо.
#
# Поэтому — отдельный пункт меню с честной рамкой вокруг: это чужой код,
# он выполняется с правами root, и результат его работы НЕ попадает в
# манифест, а значит не восстановится сам при следующей сборке хоста.

readonly KEEL_EXT_CATALOG="https://community-scripts.github.io/ProxmoxVE/scripts"
readonly KEEL_EXT_POST_INSTALL="https://raw.githubusercontent.com/community-scripts/ProxmoxVE/main/tools/pve/post-pve-install.sh"

_external_warning() {
  cat <<EOF
Это чужие сценарии, не часть keel.

Что важно понимать:

  · выполняются с правами root и могут менять что угодно;
  · их содержимое может поменяться в любой момент — код скачивается
    из интернета прямо перед запуском;
  · результат НЕ описан в твоём манифесте. Значит при следующей сборке
    хоста с нуля он не восстановится, и помнить о нём придётся тебе.

Для разовой установки приложения в контейнер — удобно.
Для того, на чём держится хост, — опиши это в манифесте.

Каталог: ${KEEL_EXT_CATALOG}
EOF
}

# Скачать сценарий, показать начало, спросить, выполнить.
# Скачивание и запуск — два разных подтверждения: между ними ты видишь,
# что именно собираешься выполнить.
external_run_script() {
  local url=$1
  [[ -n "$url" ]] || return 0

  local tmp; tmp=$(mktemp)
  if ! run "Скачать сценарий ${url##*/}" curl -fsSL -o "$tmp" "$url"; then
    keel_tmp_cleanup "$tmp"
    return 1
  fi

  if [[ ! -s "$tmp" ]]; then
    err "Скачанный файл пуст — проверь ссылку."
    keel_tmp_cleanup "$tmp"
    return 1
  fi

  local size head_text
  size=$(wc -l <"$tmp")
  head_text=$(head -n 40 "$tmp")
  ui_msgbox "Начало сценария (${size} строк)" "$head_text"

  if ! ui_yesno "Выполнить?" "Выполнить ${url##*/} с правами root?"; then
    note "Отменено, ничего не выполнено."
    keel_tmp_cleanup "$tmp"
    return 0
  fi

  run "Выполнить внешний сценарий ${url##*/}" bash "$tmp"
  local rc=$?
  keel_tmp_cleanup "$tmp"
  state_record "external" run "${url##*/}"
  return "$rc"
}

external_menu() {
  ui_msgbox "Внешние инструменты" "$(_external_warning)"

  local choice
  choice=$(ui_menu "Внешние инструменты" "Что запустить?" \
    post   "post-pve-install от community-scripts" \
    custom "Указать ссылку на сценарий вручную" \
    back   "Назад")

  case "$choice" in
    post)
      external_run_script "$KEEL_EXT_POST_INSTALL"
      ;;
    custom)
      local url
      url=$(ui_input "Ссылка на сценарий" \
        "Полный адрес .sh-файла. Ищи в каталоге:
${KEEL_EXT_CATALOG}")
      [[ -n "$url" ]] && external_run_script "$url"
      ;;
    *) return 0 ;;
  esac
}
