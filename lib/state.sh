#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: журнал
#
# Кто, когда и с каким исходом применялся. Нужен, чтобы после перезагрузки
# было видно, на чём остановились, и чтобы doctor мог показать историю.

state_init() {
  if ! mkdir -p "$KEEL_STATE_DIR" 2>/dev/null || [[ ! -w "$KEEL_STATE_DIR" ]]; then
    KEEL_STATE_DIR="${TMPDIR:-/tmp}/keel-state"
    # shellcheck disable=SC2034  # используется в lib/core.sh (keel_backup_file)
    KEEL_BACKUP_DIR="${KEEL_STATE_DIR}/backups"
    mkdir -p "$KEEL_STATE_DIR"   # keel:allow-direct каталог состояния самого keel
  fi
  KEEL_JOURNAL="${KEEL_STATE_DIR}/journal.tsv"
  [[ -f "$KEEL_JOURNAL" ]] || printf 'дата\tмодуль\tдействие\tисход\n' >"$KEEL_JOURNAL"
}

state_record() {
  local module=$1 verb=$2 status=$3
  [[ -n "${KEEL_JOURNAL:-}" ]] || return 0
  printf '%s\t%s\t%s\t%s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$module" "$verb" "$status" \
    >>"$KEEL_JOURNAL"
}

state_tail() {
  local n=${1:-15}
  [[ -f "${KEEL_JOURNAL:-}" ]] || { printf 'Журнал пуст.\n'; return 0; }
  # В файле всегда есть строка заголовка — одна она означает пустой журнал
  local rows; rows=$(tail -n +2 "$KEEL_JOURNAL")
  [[ -n "$rows" ]] || { printf 'Журнал пуст.\n'; return 0; }
  head -n1 "$KEEL_JOURNAL"
  printf '%s\n' "$rows" | tail -n "$n"
}
