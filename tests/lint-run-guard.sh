#!/usr/bin/env bash
#
# Проверка главного правила проекта: модули меняют систему только через
# run() и run_write(). Прямой вызов изменяющей команды — ошибка сборки,
# потому что такое изменение не покажется пользователю и не попадёт в лог.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Команды, которые меняют систему и потому не могут вызываться напрямую
FORBIDDEN='apt-get|apt|aptitude|dpkg|qm|pct|pvesm|pveam|pveum|pvecm|systemctl|service|rm|mv|cp|mkdir|rmdir|chmod|chown|ln|dd|mkfs|mount|umount|tee|useradd|usermod|zfs|zpool|lvcreate|lvremove|vgcreate|update-grub|proxmox-boot-tool|modprobe|sysctl'

fail=0
while IFS= read -r file; do
  lineno=0
  while IFS= read -r line; do
    lineno=$(( lineno + 1 ))
    stripped=${line#"${line%%[![:space:]]*}"}       # убрать отступ
    [[ -z "$stripped" || "$stripped" == \#* ]] && continue

    # Прямой вызов изменяющей команды в начале выражения
    if [[ "$stripped" =~ ^(${FORBIDDEN})[[:space:]] ]]; then
      printf '%s:%d: прямой вызов изменяющей команды — нужно через run(): %s\n' \
        "$file" "$lineno" "$stripped" >&2
      fail=1
    fi
    # sed -i правит файл на месте
    if [[ "$stripped" =~ (^|[^[:alnum:]_])sed[[:space:]]+-i ]]; then
      printf '%s:%d: sed -i правит файл мимо run_write(): %s\n' "$file" "$lineno" "$stripped" >&2
      fail=1
    fi
    # Запись в системные каталоги через перенаправление
    if [[ "$stripped" =~ \>\>?[[:space:]]*\"?/(etc|usr|var|boot|opt|lib)/ ]]; then
      printf '%s:%d: запись в системный путь мимо run_write(): %s\n' "$file" "$lineno" "$stripped" >&2
      fail=1
    fi
  done < "$file"
done < <(find modules -type f -name '*.sh' 2>/dev/null)

if (( fail )); then
  printf '\nПравило нарушено: изменения системы идут только через run()/run_write().\n' >&2
  exit 1
fi
printf 'run-guard: модули не меняют систему в обход run() — порядок.\n'
