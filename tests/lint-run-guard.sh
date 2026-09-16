#!/usr/bin/env bash
#
# Проверка главного правила проекта: модули меняют систему только через
# run() и run_write(). Прямой вызов изменяющей команды — ошибка,
# потому что такое изменение не покажется пользователю и не попадёт в лог.
#
# Переносы строк склеиваются: аргументы run(), вынесенные на следующую
# строку, — это часть вызова run(), а не отдельная команда.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

# Команды, которые меняют систему и потому не могут вызываться напрямую
FORBIDDEN='apt-get|apt|aptitude|dpkg|qm|pct|pvesm|pveam|pveum|pvecm|pvesh|systemctl|service|rm|mv|cp|mkdir|rmdir|chmod|chown|ln|dd|mkfs|mount|umount|tee|useradd|usermod|zfs|zpool|lvcreate|lvremove|vgcreate|update-grub|proxmox-boot-tool|modprobe|sysctl|curl|wget'

fail=0

check_statement() {
  local file=$1 lineno=$2 stmt=$3
  [[ -z "$stmt" || "$stmt" == \#* ]] && return 0

  # Команда может прятаться после &&, ||, ; или | — проверяем каждый кусок
  local segment
  while IFS= read -r segment; do
    segment=${segment#"${segment%%[![:space:]]*}"}
    [[ -n "$segment" ]] || continue
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
done < <(find modules -type f -name '*.sh' 2>/dev/null)

if (( fail )); then
  printf '\nПравило нарушено: изменения системы идут только через run()/run_write().\n' >&2
  exit 1
fi
printf 'run-guard: модули не меняют систему в обход run() — порядок.\n'
