#!/usr/bin/perl
#
# keel :: веб-интерфейс
#
# HTTP-сервер на самом хосте. Не контейнер и не дашборд: то же самое, что
# делает терминал, показанное в браузере. План, подтверждение, поток
# применения — один и тот же keel, просто другой экран.
#
# Почему на ядерном Perl. На Proxmox perl есть всегда (сам PVE на нём
# написан), а IO::Socket::INET, IO::Select, JSON::PP и Digest::SHA входят
# в стандартную поставку. HTTP::Daemon в неё не входит — и не нужен:
# минимальный HTTP/1.1 для одного клиента укладывается в эти сто строк.
# Ни одного apt install, ни одного скачанного бинарника.
#
# Границы, которые тут важнее возможностей:
#
#   · сервер слушает 127.0.0.1, пока явно не сказано иначе;
#   · без токена не отвечает ничего, даже на localhost — на хосте есть и
#     другие пользователи, а этот сервер работает под root;
#   · терминальный путь остаётся главным. Пока Proxmox не поднялся,
#     запускать HTTP-сервер негде, и восстановление идёт из консоли.

use strict;
use warnings;

package Keel::Web;

use IO::Socket::INET;
use JSON::PP ();
use Digest::SHA qw(sha256_hex);
use POSIX qw(:sys_wait_h);
use Fcntl qw(O_WRONLY O_CREAT O_EXCL);

our $ROOT;       # каталог с кодом keel
our $TOKEN;      # секрет, без которого сервер молчит
our $HOME;       # каталог данных keel (для файла-замка)

# Статика отдаётся по списку, а не по имени из запроса. Это не перестраховка:
# сервер работает под root, и любой разбор пути — это потенциальный выход из
# каталога. Список из трёх файлов такого класса ошибок не имеет вовсе.
our %STATIC = (
  '/'           => [ 'index.html', 'text/html; charset=utf-8' ],
  '/app.js'     => [ 'app.js',     'application/javascript; charset=utf-8' ],
  '/style.css'  => [ 'style.css',  'text/css; charset=utf-8' ],
);

# --- Разбор запроса ----------------------------------------------------------
#
# Отдельной функцией от сокета: так её можно проверить тестом, не поднимая
# сети. Возвращает undef на всём, что не похоже на HTTP-запрос.
sub parse_request {
  my ($text) = @_;
  return undef unless defined $text && length $text;

  my ($head) = split /\r?\n\r?\n/, $text, 2;
  my @lines = split /\r?\n/, $head;
  my $first = shift @lines;
  return undef unless defined $first;
  return undef unless $first =~ m{^([A-Z]+)[ ]+(\S+)[ ]+HTTP/1\.[01]$};

  my ($method, $target) = ($1, $2);
  my ($path, $qs) = split /\?/, $target, 2;
  $qs = '' unless defined $qs;

  my %query;
  for my $pair (split /&/, $qs) {
    next unless length $pair;
    my ($k, $v) = split /=/, $pair, 2;
    $v = '' unless defined $v;
    for ($k, $v) { tr/+/ /; s/%([0-9A-Fa-f]{2})/chr(hex($1))/ge; }
    $query{$k} = $v;
  }

  my %headers;
  for my $line (@lines) {
    next unless $line =~ /^([^:]+):\s*(.*)$/;
    $headers{ lc $1 } = $2;
  }

  return { method => $method, path => $path, query => \%query, headers => \%headers };
}

# Сравнение токенов через дайджест: длина и содержимое настоящего секрета
# не влияют на время ответа, поэтому подбирать его по таймингу нечего.
sub token_ok {
  my ($given) = @_;
  return 0 unless defined $given && length $given;
  return 0 unless defined $TOKEN && length $TOKEN;
  return sha256_hex($given) eq sha256_hex($TOKEN) ? 1 : 0;
}

# Идентификатор модуля или список id гостей — из запроса наружу, значит
# проверяются по белому списку символов, а не «очищаются».
sub safe_only {
  my ($v) = @_;
  return '' unless defined $v && length $v;
  return $v =~ m{^[a-z0-9/_-]+$} ? $v : '';
}

sub safe_guests {
  my ($v) = @_;
  return '' unless defined $v && length $v;
  return $v =~ m{^[0-9]+(,[0-9]+)*$} ? $v : '';
}

# --- Ответы ------------------------------------------------------------------

sub send_head {
  my ($sock, $status, $type, %extra) = @_;
  my $out = "HTTP/1.1 $status\r\n";
  $out .= "Content-Type: $type\r\n" if defined $type;
  # Страница управляет гипервизором: ни кэшей, ни чужих фреймов, ни утечки
  # токена в Referer на сторонние адреса.
  $out .= "Cache-Control: no-store\r\n";
  $out .= "X-Content-Type-Options: nosniff\r\n";
  $out .= "X-Frame-Options: DENY\r\n";
  $out .= "Referrer-Policy: no-referrer\r\n";
  $out .= "$_: $extra{$_}\r\n" for sort keys %extra;
  $out .= "Connection: close\r\n\r\n";
  print $sock $out;
}

sub send_text {
  my ($sock, $status, $type, $body) = @_;
  send_head($sock, $status, $type, 'Content-Length' => length $body);
  print $sock $body;
}

# Один кусок chunked-потока. Пустую строку не шлём: нулевой размер — это
# признак конца ответа, и случайно отправить его посреди вывода нельзя.
sub send_chunk {
  my ($sock, $data) = @_;
  return unless defined $data && length $data;
  printf $sock "%x\r\n%s\r\n", length($data), $data;
}

sub end_chunks {
  my ($sock) = @_;
  print $sock "0\r\n\r\n";
}

sub send_json {
  my ($sock, $status, $data) = @_;
  my $body = JSON::PP->new->canonical->utf8->encode($data);
  send_text($sock, $status, 'application/json; charset=utf-8', $body);
}

sub cookie_of {
  my ($req, $want) = @_;
  my $raw = $req->{headers}{cookie};
  return undef unless defined $raw;
  for my $part (split /;\s*/, $raw) {
    my ($k, $v) = split /=/, $part, 2;
    next unless defined $k && defined $v;
    $k =~ s/^\s+|\s+$//g;
    return $v if $k eq $want;
  }
  return undef;
}

sub send_file {
  my ($sock, $name, $type, $set_cookie) = @_;
  my $path = "$ROOT/web/$name";
  my $fh;
  if (!open($fh, '<', $path)) {
    send_text($sock, '404 Not Found', 'text/plain; charset=utf-8', "нет файла $name\n");
    return;
  }
  binmode $fh;
  my $body = do { local $/; <$fh> };
  close $fh;

  my %extra = ('Content-Length' => length $body);
  if ($set_cookie) {
    # SameSite=Strict здесь не украшение: /api/apply меняет хост, и без него
    # чужая страница могла бы отправить на него запрос от имени открытой
    # вкладки. HttpOnly — чтобы кука не досталась скрипту.
    $extra{'Set-Cookie'} = "keel=$TOKEN; Path=/; HttpOnly; SameSite=Strict";
  }
  send_head($sock, '200 OK', $type, %extra);
  print $sock $body;
}

# --- Запуск keel с потоковым выводом -----------------------------------------
#
# Ответ уходит без Content-Length и закрывается по концу вывода: длину
# dist-upgrade заранее не знает никто, а ждать её конца, чтобы отдать всё
# разом, — значит смотреть на пустой экран те самые десять минут.
#
# Браузер читает это обычным fetch со стримом, не EventSource. Разница
# принципиальная: EventSource переподключается сам, а переподключение к
# /api/apply означало бы второй запуск применения.
sub stream_keel {
  my ($sock, $args, $env) = @_;
  # chunked, а не «просто закроем сокет в конце». Длину знать не нужно ни в
  # том, ни в другом случае, но у chunked есть явный признак конца — нулевой
  # кусок. Без него браузер видит обрыв соединения и считает ответ
  # прерванным: данные доходят, но каждый запрос отмечается как упавший.
  send_head($sock, '200 OK', 'text/plain; charset=utf-8',
    'Transfer-Encoding' => 'chunked');

  my @cmd = ("$ROOT/bin/keel", '--plain', @$args);
  # Без модификатора if: «local %H = ... if ...» — известная ловушка Perl,
  # там local выполняется один раз, а присваивание условно
  local %ENV = (%ENV, %{ $env || {} });

  my $out;
  my $pid = open($out, '-|');
  if (!defined $pid) {
    send_chunk($sock, "не удалось запустить keel: $!\n");
    end_chunks($sock);
    return;
  }
  if (!$pid) {
    # Ребёнок: stderr тоже на экран страницы — предупреждения keel не менее
    # важны, чем его обычный вывод
    open(STDERR, '>&', \*STDOUT);
    exec { $cmd[0] } @cmd or do {
      print "не удалось запустить $cmd[0]: $!\n";
      exit 127;
    };
  }

  my $old = select $sock; $| = 1; select $old;
  while (my $line = <$out>) {
    send_chunk($sock, $line);
  }
  close $out;
  end_chunks($sock);
}

# Применение — единственное, что меняет хост, и делать это одновременно из
# двух вкладок нельзя. Замок файловый, а не в памяти: соединения обслуживают
# разные процессы, общей памяти у них нет.
sub with_apply_lock {
  my ($sock, $code) = @_;
  my $lock = "$HOME/web-apply.lock";
  my $fh;
  if (!sysopen($fh, $lock, O_WRONLY | O_CREAT | O_EXCL, 0600)) {
    send_text($sock, '409 Conflict', 'text/plain; charset=utf-8',
      "Применение уже идёт — в другой вкладке или из терминала.\n" .
      "Если это не так, замок остался от прерванного запуска: $lock\n");
    return;
  }
  print $fh "$$\n";
  close $fh;
  eval { $code->(); 1 } or do { my $e = $@; unlink $lock; die $e };
  unlink $lock;
}

# --- Маршруты ----------------------------------------------------------------

sub handle {
  my ($sock, $req) = @_;
  my $path = $req->{path};

  # Токен спрашивается раньше маршрута: неавторизованный не должен даже
  # узнать, какие адреса тут есть.
  #
  # Источников три, и третий обязателен. Токен приходит в адресе страницы, но
  # style.css и app.js браузер запрашивает сам, обычными относительными
  # ссылками — без строки запроса. Поэтому удачный вход за страницу выдаёт
  # куку, и дальше ею же авторизуются подресурсы и fetch.
  my $ok = 0;
  for my $cand ($req->{query}{t}, $req->{headers}{'x-keel-token'}, cookie_of($req, 'keel')) {
    $ok = 1 if token_ok($cand);
  }
  if (!$ok) {
    send_text($sock, '401 Unauthorized', 'text/plain; charset=utf-8',
      "Нужен токен. Открывай тот адрес, который keel web напечатал при запуске.\n");
    return;
  }

  if (my $st = $STATIC{$path}) {
    return send_file($sock, $st->[0], $st->[1], $path eq '/');
  }

  # Значка у страницы нет, и заводить его незачем. Но браузер просит его сам,
  # и 404 в консоли выглядит как поломка, которой нет.
  if ($path eq '/favicon.ico') {
    send_head($sock, '204 No Content', undef, 'Content-Length' => 0);
    return;
  }

  if ($path eq '/api/state') {
    my $host = `hostname 2>/dev/null`;
    chomp $host;
    return send_json($sock, '200 OK', {
      version  => $ENV{KEEL_VERSION} // '?',
      host     => $host || '?',
      manifest => $ENV{KEEL_MANIFEST} // '',
      home     => $HOME,
    });
  }

  # Читающие команды. Ничего не меняют, поэтому и замок им не нужен.
  my %READONLY = (
    '/api/plan'   => ['plan'],
    '/api/doctor' => ['doctor'],
    '/api/verify' => ['verify'],
    '/api/guests' => ['guests'],
  );
  if (my $args = $READONLY{$path}) {
    my @a = @$args;
    my $only = safe_only($req->{query}{only});
    push @a, '--only', $only if length $only;
    return stream_keel($sock, \@a);
  }

  if ($path eq '/api/apply') {
    if ($req->{method} ne 'POST') {
      return send_text($sock, '405 Method Not Allowed', 'text/plain; charset=utf-8',
        "Применение — только POST.\n");
    }
    my @a = ('apply', '--yes');
    my $only = safe_only($req->{query}{only});
    push @a, '--only', $only if length $only;
    my %env;
    my $guests = safe_guests($req->{query}{guests});
    $env{KEEL_GUESTS_ONLY} = $guests if length $guests;
    return with_apply_lock($sock, sub { stream_keel($sock, \@a, \%env) });
  }

  send_text($sock, '404 Not Found', 'text/plain; charset=utf-8', "нет такого адреса\n");
}

# --- Цикл приёма -------------------------------------------------------------

sub read_request {
  my ($sock) = @_;
  my $buf = '';
  # Заголовок запроса не бывает длинным: всё, что больше, — либо ошибка,
  # либо попытка занять память сервера
  while (length($buf) < 16384) {
    my $chunk;
    my $n = sysread($sock, $chunk, 4096);
    last unless defined $n && $n > 0;
    $buf .= $chunk;
    last if $buf =~ /\r?\n\r?\n/;
  }
  return $buf;
}

sub main {
  # Без этого строка READY не выйдет наружу, пока буфер не наполнится, —
  # то есть в трубу или в файл она не попадёт вовсе, а именно оттуда её
  # и читают: и тесты, и человек, запустивший сервер не в терминале
  $| = 1;
  my %opt = (listen => '127.0.0.1', port => 8777);
  my @argv = @_;
  while (@argv) {
    my $a = shift @argv;
    if    ($a eq '--listen') { $opt{listen} = shift @argv }
    elsif ($a eq '--port')   { $opt{port}   = shift @argv }
    elsif ($a eq '--token')  { $TOKEN       = shift @argv }
    elsif ($a eq '--root')   { $ROOT        = shift @argv }
    elsif ($a eq '--home')   { $HOME        = shift @argv }
    else { die "неизвестный аргумент: $a\n" }
  }
  die "не задан --root\n"  unless defined $ROOT  && length $ROOT;
  die "не задан --token\n" unless defined $TOKEN && length $TOKEN;
  $HOME = $ROOT unless defined $HOME && length $HOME;

  my $server = IO::Socket::INET->new(
    LocalAddr => $opt{listen},
    LocalPort => $opt{port},
    Proto     => 'tcp',
    Listen    => 16,
    ReuseAddr => 1,
  ) or die "не удалось занять $opt{listen}:$opt{port}: $!\n";

  # Порт мог быть нулём — тогда его выбрало ядро, и знать какой важно
  printf "READY %s %d\n", $opt{listen}, $server->sockport;

  $SIG{CHLD} = sub { while (waitpid(-1, WNOHANG) > 0) {} };

  while (1) {
    my $sock = $server->accept;
    # Пустой ответ accept — это почти всегда EINTR: обработчик SIGCHLD,
    # прибирающий отработавшего ребёнка, прерывает системный вызов. Писать
    # «while (my $sock = accept)» здесь нельзя: первый же закрывшийся
    # клиент гасил бы весь сервер.
    next unless defined $sock;

    my $pid = fork();
    if (!defined $pid) {
      close $sock;
      next;
    }
    if ($pid) {
      close $sock;
      next;
    }
    # Ребёнок обслуживает одно соединение и уходит. Форк здесь не ради
    # нагрузки, а ради живучести: applied-процесс может идти минутами, и
    # всё это время сервер обязан отвечать другим вкладкам.
    close $server;
    $SIG{CHLD} = 'DEFAULT';
    my $req = parse_request(read_request($sock));
    if ($req) {
      eval { handle($sock, $req); 1 } or do {
        print $sock "\nошибка сервера: $@\n";
      };
    } else {
      send_text($sock, '400 Bad Request', 'text/plain; charset=utf-8', "не HTTP-запрос\n");
    }

    # Закрываем мягко: сначала «писать больше не буду», потом дочитываем то,
    # что клиент успел прислать. Закрыть сокет с непрочитанным входом —
    # значит отправить RST, и браузер отмечает полностью доставленный ответ
    # как прерванный. Будильник — на случай клиента, который свою сторону
    # так и не закроет: висеть в ожидании ребёнку незачем.
    $SIG{ALRM} = sub { exit 0 };
    alarm 5;
    shutdown($sock, 1);
    my $drain;
    1 while sysread($sock, $drain, 4096);
    alarm 0;
    close $sock;
    exit 0;
  }
}

package main;

# При require из теста ничего не запускается — только определяются функции
Keel::Web::main(@ARGV) unless caller();

1;
