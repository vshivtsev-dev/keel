#!/usr/bin/env bash
#
# Страж приватности.
#
# Репозиторий открытый. Значит всё, что в него попало, видно всем — включая
# то, что попало случайно. Эта проверка ищет в отслеживаемых git-ом файлах
# следы настоящей инфраструктуры: адреса, почты, ключи, токены.
#
# Выдуманные примеры из документации перечислены в tests/allowed-examples.txt,
# по строке на совпадение. Новое совпадение не проходит молча — его нужно
# либо убрать, либо осознанно внести в список.

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

ALLOW="tests/allowed-examples.txt"

# Что считаем следом настоящей инфраструктуры
PATTERNS='\b(192\.168\.[0-9]{1,3}\.[0-9]{1,3}|10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]{1,3}\.[0-9]{1,3})\b'
PATTERNS+='|[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'
PATTERNS+='|ssh-(rsa|ed25519) AAAA[A-Za-z0-9+/=]*'
PATTERNS+='|-----BEGIN [A-Z ]*PRIVATE KEY-----'
PATTERNS+='|ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}'
PATTERNS+='|AKIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9-]{10,}'

fail=0

allowed() {
  local key=$1
  [[ -f "$ALLOW" ]] || return 1
  grep -qxF -- "$key" "$ALLOW"
}

# 1. Личные данные в содержимом файлов
while IFS= read -r file; do
  [[ -f "$file" ]] || continue
  # Сам список исключений и этот файл не сканируем: в них совпадения по смыслу
  [[ "$file" == "$ALLOW" || "$file" == "tests/lint-no-secrets.sh" ]] && continue

  while IFS= read -r match; do
    [[ -n "$match" ]] || continue
    allowed "${file}:${match}" && continue
    printf '%s: похоже на настоящие данные: %s\n' "$file" "$match" >&2
    fail=1
  done < <(grep -hoE "$PATTERNS" "$file" 2>/dev/null | sort -u)
done < <({ # core.quotePath=false: иначе git отдаёт кириллические имена как
           # "docs/\320\222…", такой путь не открыть, и файл молча не проверится
           git -c core.quotePath=false ls-files 2>/dev/null
           # Новые файлы ещё не в индексе, а личные данные в них уже есть.
           # Именно так в репозиторий однажды и уехал пример с адресом.
           git -c core.quotePath=false ls-files --others --exclude-standard 2>/dev/null
         } | sort -u)

# 2. Манифест хоста не должен попадать в git никогда
if git ls-files 2>/dev/null | grep -qx 'manifest/host.json'; then
  printf 'manifest/host.json отслеживается git — в нём твои адреса и пути к ключам\n' >&2
  fail=1
fi

# 3. И должен оставаться в .gitignore
if ! grep -qx 'manifest/host.json' .gitignore 2>/dev/null; then
  printf 'manifest/host.json пропал из .gitignore — он туда обязан вернуться\n' >&2
  fail=1
fi

if (( fail )); then
  printf '\nЛибо убери это из репозитория, либо внеси в %s осознанно.\n' "$ALLOW" >&2
  exit 1
fi
printf 'no-secrets: личных данных в файлах репозитория нет — порядок.\n'
