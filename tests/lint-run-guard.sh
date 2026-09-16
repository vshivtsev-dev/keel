#!/usr/bin/env bash
#
# Проверка главного правила проекта: модули меняют систему только через
# run() и run_write(). Прямой вызов изменяющей команды — ошибка,
# потому что такое изменение не покажется пользователю и не попадёт в лог.
#
# Переносы строк склеиваются: аргументы run(), вынесенные на следующую
# строку, — это часть вызова run(), а не отдельная команда.
#
# Смотрим и в modules/, и в lib/: модули тонкие, а работа живёт в библиотеках,
# и именно там правило легче всего обойти незаметно. Исключение — lib/core.sh:
# там сами ворота. Отдельные честные строки помечаются комментарием
# «keel:allow-direct <причина>».

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

# Команды, которые меняют систему и потому не могут вызываться напрямую
# Те же команды в читающих подкомандах систему не меняют: doctor и проверки
# модулей обязаны спрашивать хост напрямую, иначе им нечего показывать.
READONLY='pvesm (status|list|path)|pvesh get|qm (list|config|status|showcmd)|pct (list|config|status|help)|pveam (list|available)|proxmox-boot-tool status|zpool (list|status)|zfs (list|get)|apt list|apt-cache|dpkg -[ls]|dpkg-query|systemctl (is-active|is-enabled|show|list-unit-files)'

FORBIDDEN='apt-get|apt|aptitude|dpkg|qm|pct|pvesm|pveam|pveum|pvecm|pvesh|systemctl|service|rm|mv|cp|mkdir|rmdir|chmod|chown|ln|dd|mkfs|mount|umount|tee|useradd|usermod|zfs|zpool|lvcreate|lvremove|vgcreate|update-grub|proxmox-boot-tool|modprobe|sysctl|curl|wget'

fail=0

check_statement() {
  local file=$1 lineno=$2 stmt=$3
  [[ -z "$stmt" || "$stmt" == \#* ]] && return 0

  # Осознанное исключение: работа с собственными временными файлами и
  # каталогом состояния keel. Пометка обязана называть причину — так каждое
  # исключение видно глазами при чтении кода.
  [[ "$stmt" == *"keel:allow-direct"* ]] && return 0

  # Команда может прятаться после &&, ||, ; или | — проверяем каждый кусок
  local segment
  while IFS= read -r segment; do
    segment=${segment#"${segment%%[![:space:]]*}"}
    [[ -n "$segment" ]] || continue
    if [[ "$segment" =~ ^(${READONLY})([[:space:]]|$) ]]; then continue; fi
    if [[ "$segment" =~ ^(${FORBIDDEN})[[:space:]] ]]; then
      printf '%s:%d: прямой вызов изменяющей команды — нужно через run(): %s\n' \
        "$file" "$lineno" "$segment" >&2
      fail=1
    fi
  done < <(printf '%s\n' "$stmt" | sed 's/&&/\n/g; s/||/\n/g; s/;/\n/g')
  if [[ "$stmt" =~ (^|[^[:alnum:]_])sed[[:space:]]+-i ]]; then
    printf '%s:%d: sed -i правит файл мимо run_write(): %s\n' "$file" "$lineno" "$stmt" >&2
    fail=1
  fi
  if [[ "$stmt" =~ \>\>?[[:space:]]*\"?/(etc|usr|var|boot|opt|lib)/ ]]; then
    printf '%s:%d: запись в системный путь мимо run_write(): %s\n' "$file" "$lineno" "$stmt" >&2
    fail=1
  fi
  return 0
}

while IFS= read -r file; do
  lineno=0
  stmt=""
  stmt_line=0
  while IFS= read -r line; do
    lineno=$(( lineno + 1 ))
    stripped=${line#"${line%%[![:space:]]*}"}
    if [[ -z "$stmt" ]]; then stmt_line=$lineno; fi
    if [[ "$line" == *\\ ]]; then
      # Строка продолжается: копим её и ждём конца выражения
      stmt+="${stripped%\\} "
      continue
    fi
    stmt+="$stripped"
    check_statement "$file" "$stmt_line" "$stmt"
    stmt=""
  done < "$file"
done < <(find modules lib -type f -name '*.sh' 2>/dev/null | grep -v '^lib/core\.sh$' | sort)

if (( fail )); then
  printf '\nПравило нарушено: изменения системы идут только через run()/run_write().\n' >&2
  exit 1
fi
printf 'run-guard: модули и библиотеки не меняют систему в обход run() — порядок.\n'
