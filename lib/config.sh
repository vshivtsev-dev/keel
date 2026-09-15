#!/usr/bin/env bash
# shellcheck shell=bash
#
# keel :: манифест
#
# Манифест — JSON с комментариями. Читается модулем JSON::PP, который входит
# в стандартную поставку Perl, а Perl есть на любом Proxmox: значит парсер
# доступен всегда, без единого apt install и без сети.
#
# Режим relaxed разрешает комментарии в стиле shell (# до конца строки).
# Если на конкретном хосте relaxed недоступен, config_load сам срежет
# комментарии запасным способом и скажет об этом в лог.
#
# Внутри JSON разворачивается в плоские ключи:
#   guests[0].name      ->  KEEL_CFG[guests.0.name]
#   длина массива       ->  KEEL_CFG[guests.__len]
#   ключи объекта       ->  KEEL_CFG[host.__keys]

declare -gA KEEL_CFG=()
KEEL_CFG_LOADED=0
KEEL_MANIFEST="${KEEL_MANIFEST:-}"
KEEL_JSON_RELAXED=""

# Верхнеуровневые ключи, которые keel понимает. Всё остальное — опечатка.
readonly KEEL_CFG_TOPLEVEL="host storages guests backup"

_config_perl_flatten() {
  perl -e '
use strict; use warnings; use JSON::PP;
my $txt = do { local $/; <STDIN> };
my $json = JSON::PP->new;
$json = $json->relaxed if $ENV{KEEL_RELAXED};
my $data = eval { $json->decode($txt) };
if ($@) { my $e = $@; $e =~ s/\s+$//; print STDERR "$e\n"; exit 2; }
sub emit { my ($k, $v) = @_; print $k, "=", $v, "\0"; }
sub walk {
  my ($prefix, $node) = @_;
  my $r = ref $node;
  if ($r eq "HASH") {
    my @k = sort keys %$node;
    emit($prefix eq "" ? "__keys" : "$prefix.__keys", join(",", @k));
    walk($prefix eq "" ? $_ : "$prefix.$_", $node->{$_}) for @k;
  } elsif ($r eq "ARRAY") {
    emit($prefix eq "" ? "__len" : "$prefix.__len", scalar(@$node));
    walk($prefix eq "" ? "$_" : "$prefix.$_", $node->[$_]) for (0 .. $#$node);
  } elsif (!defined $node) {
    emit($prefix, "");
  } elsif (JSON::PP::is_bool($node)) {
    emit($prefix, $node ? "true" : "false");
  } else {
    emit($prefix, "$node");
  }
}
walk("", $data);
'
}

# Есть ли вообще чем читать манифест
config_parser_available() {
  perl -MJSON::PP -e 'exit 0' >/dev/null 2>&1
}

# Поддерживает ли JSON::PP на этом хосте комментарии (режим relaxed).
# Результат кэшируется в KEEL_JSON_RELAXED: yes | no
config_relaxed_supported() {
  if [[ -n "$KEEL_JSON_RELAXED" ]]; then
    [[ "$KEEL_JSON_RELAXED" == "yes" ]]
    return
  fi
  if printf '{ # комментарий\n"a": 1 }' \
      | perl -MJSON::PP -e 'local $/; JSON::PP->new->relaxed->decode(<STDIN>)' >/dev/null 2>&1
  then
    KEEL_JSON_RELAXED="yes"
  else
    KEEL_JSON_RELAXED="no"
  fi
  [[ "$KEEL_JSON_RELAXED" == "yes" ]]
}

# Запасной путь: срезать # -комментарии, не тронув то, что внутри строк
# (иначе развалятся значения вроде "https://example.com/#anchor").
_config_strip_comments() {
  perl -pe '
    chomp;
    my $out = ""; my $in_str = 0; my $esc = 0;
    for my $c (split //, $_) {
      if ($esc)            { $out .= $c; $esc = 0; next; }
      if ($c eq "\\")      { $out .= $c; $esc = 1; next; }
      if ($c eq q{"})      { $in_str = !$in_str; $out .= $c; next; }
      if ($c eq "#" && !$in_str) { last; }
      $out .= $c;
    }
    $_ = $out . "\n";
  '
}

# config_load [путь]
# Нет файла — это не ошибка: модули просто останутся ненастроенными.
config_load() {
  local path=${1:-$KEEL_MANIFEST}
  KEEL_CFG=()
  KEEL_CFG_LOADED=0

  [[ -n "$path" ]] || return 0
  if [[ ! -f "$path" ]]; then
    keel_log "CONFIG: манифест ${path} не найден — работаем без него"
    return 0
  fi
  config_parser_available || die "Не найден Perl с модулем JSON::PP — читать манифест нечем."

  local src rec key val
  if config_relaxed_supported; then
    src=$(cat "$path")
    export KEEL_RELAXED=1
  else
    keel_log "CONFIG: relaxed недоступен, срезаю комментарии запасным способом"
    src=$(_config_strip_comments <"$path")
    unset KEEL_RELAXED
  fi

  local errfile; errfile=$(mktemp)
  while IFS= read -r -d '' rec; do
    key=${rec%%=*}
    val=${rec#*=}
    KEEL_CFG["$key"]=$val
  done < <(printf '%s' "$src" | _config_perl_flatten 2>"$errfile")

  if [[ -s "$errfile" ]]; then
    local msg; msg=$(cat "$errfile"); rm -f "$errfile"
    die "Манифест ${path} не читается: ${msg}"
  fi
  rm -f "$errfile"

  KEEL_CFG_LOADED=1
  KEEL_MANIFEST=$path
  keel_log "CONFIG: загружен ${path} (ключей: ${#KEEL_CFG[@]})"
  return 0
}

config_has() { [[ -n "${KEEL_CFG[$1]+x}" ]]; }

config_get() {
  local key=$1 default=${2:-}
  if [[ -n "${KEEL_CFG[$key]+x}" ]]; then
    printf '%s' "${KEEL_CFG[$key]}"
  else
    printf '%s' "$default"
  fi
}

# Служебный ключ с учётом корня: _config_meta "" __keys -> "__keys"
_config_meta() {
  local prefix=$1 suffix=$2
  if [[ -z "$prefix" ]]; then printf '%s' "$suffix"; else printf '%s.%s' "$prefix" "$suffix"; fi
}

config_len() { config_get "$(_config_meta "$1" __len)" 0; }

config_keys() {
  local raw; raw=$(config_get "$(_config_meta "$1" __keys)" "")
  [[ -n "$raw" ]] || return 0
  printf '%s\n' "${raw//,/$'\n'}"
}

config_bool() {
  local v; v=$(config_get "$1" "${2:-false}")
  [[ "$v" == "true" || "$v" == "1" ]]
}

# Проверка манифеста: понятные ошибки вместо "parse error".
# Возвращает 0, если всё в порядке.
config_validate() {
  (( KEEL_CFG_LOADED )) || { warn "Манифест не загружен — проверять нечего."; return 0; }
  local rc=0 key
  while read -r key; do
    [[ -n "$key" ]] || continue
    if [[ " ${KEEL_CFG_TOPLEVEL} " != *" ${key} "* ]]; then
      err "Неизвестный раздел манифеста: «${key}». Допустимые: ${KEEL_CFG_TOPLEVEL}"
      rc=1
    fi
  done < <(config_keys "")

  local n i id name
  n=$(config_len guests)
  for (( i = 0; i < n; i++ )); do
    id=$(config_get "guests.${i}.id" "")
    name=$(config_get "guests.${i}.name" "")
    if [[ -z "$id" ]]; then
      err "guests[${i}] (${name:-без имени}): не указан обязательный id"
      rc=1
    elif [[ ! "$id" =~ ^[0-9]+$ ]] || (( id < 100 )); then
      err "guests[${i}]: id должен быть числом не меньше 100, получено «${id}»"
      rc=1
    fi
    if [[ -z "$(config_get "guests.${i}.profile" "")" ]]; then
      err "guests[${i}] (${name:-без имени}): не указан profile"
      rc=1
    fi
  done
  return "$rc"
}

# --- Разбор произвольного JSON ----------------------------------------------
#
# Нужен для ответов pvesh: тот же парсер, что и для манифеста, никаких jq.

# json_pairs <<<"$json" — печатает key=value, записи разделены нулевым байтом
json_pairs() {
  KEEL_RELAXED=1 _config_perl_flatten
}

# json_get "$json" "путь.к.ключу" ["по умолчанию"]
json_get() {
  local json=$1 want=$2 default=${3:-} rec
  while IFS= read -r -d '' rec; do
    if [[ "${rec%%=*}" == "$want" ]]; then
      printf '%s' "${rec#*=}"
      return 0
    fi
  done < <(printf '%s' "$json" | json_pairs 2>/dev/null)
  printf '%s' "$default"
}

# json_len "$json" "путь" — длина массива
json_len() { json_get "$1" "$(_config_meta "$2" __len)" 0; }

# --- Профили гостей ----------------------------------------------------------
#
# Профиль — «рецепт»: откуда образ, какие дефолты, какого рода гость.
# Лежит в profiles/<имя>.json и читается тем же парсером.

declare -gA KEEL_PROF=()
KEEL_PROF_NAME=""

profile_load() {
  local name=$1
  local path="${KEEL_ROOT}/profiles/${name}.json"
  KEEL_PROF=()
  KEEL_PROF_NAME=""
  [[ -f "$path" ]] || { err "Нет профиля «${name}» (ожидался файл ${path})"; return 1; }

  local src rec
  if config_relaxed_supported; then
    src=$(cat "$path"); export KEEL_RELAXED=1
  else
    src=$(_config_strip_comments <"$path"); unset KEEL_RELAXED
  fi

  local errfile; errfile=$(mktemp)
  while IFS= read -r -d '' rec; do
    KEEL_PROF["${rec%%=*}"]=${rec#*=}
  done < <(printf '%s' "$src" | _config_perl_flatten 2>"$errfile")

  if [[ -s "$errfile" ]]; then
    local msg; msg=$(cat "$errfile"); rm -f "$errfile"
    err "Профиль ${path} не читается: ${msg}"
    return 1
  fi
  rm -f "$errfile"
  KEEL_PROF_NAME=$name
  return 0
}

# Имя загруженного профиля — модулям нужно для сообщений
profile_name() { printf '%s' "$KEEL_PROF_NAME"; }

prof_get() {
  local key=$1 default=${2:-}
  if [[ -n "${KEEL_PROF[$key]+x}" ]]; then printf '%s' "${KEEL_PROF[$key]}"
  else printf '%s' "$default"; fi
}

prof_len() { prof_get "$1.__len" 0; }

prof_bool() {
  local v; v=$(prof_get "$1" "${2:-false}")
  [[ "$v" == "true" || "$v" == "1" ]]
}

profile_list() {
  local f
  for f in "${KEEL_ROOT}"/profiles/*.json; do
    [[ -f "$f" ]] || continue
    f=${f##*/}
    printf '%s\n' "${f%.json}"
  done
}
