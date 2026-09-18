// keel :: веб-интерфейс
//
// Логики тут нет намеренно. Страница не решает, что произойдёт с хостом:
// она показывает поток, который отдаёт keel, и отправляет обратно ровно два
// намерения — «покажи» и «применяй». Всё остальное — план, правило нуля,
// порядок модулей, отказ применять вне Proxmox — остаётся на стороне keel,
// потому что второй экземпляр этих правил однажды разойдётся с первым.

'use strict';

// Токен приходит в адресе, которым keel web поделился при запуске. Дальше он
// живёт только в памяти вкладки и уходит заголовком, а не в строке запроса:
// строка запроса попадает в логи и в историю браузера.
const TOKEN = new URLSearchParams(location.search).get('t') || '';

const $log = document.getElementById('log');
const $state = document.getElementById('state');
const $hint = document.getElementById('hint');
const $apply = document.getElementById('apply');
const $stop = document.getElementById('stop');

let running = null;          // AbortController текущего запроса
let planSeen = false;        // план показывали — значит, есть с чем соглашаться

// Разметка строки по тем же значкам, которыми keel размечает вывод в
// терминале. Это чтение вывода, а не вторая его версия.
function classOf(line) {
  if (/^→/.test(line)) return 'l-run';
  if (/^✓/.test(line)) return 'l-ok';
  if (/^!/.test(line)) return 'l-warn';
  if (/^✗/.test(line)) return 'l-err';
  if (/^·/.test(line)) return 'l-skip';
  if (/^ {4}/.test(line)) return 'l-detail';
  if (/^\S.*[^:]$/.test(line) && line.length < 60 && !/[.,]$/.test(line)) return 'l-head';
  return '';
}

function clearLog() { $log.textContent = ''; }

function append(text) {
  // Прокрутку держим внизу, только если человек и так смотрел вниз: иначе
  // он читает середину вывода, а страница дёргает его обратно
  const atBottom = $log.scrollHeight - $log.scrollTop - $log.clientHeight < 40;

  // Последний кусок split — это хвост после финального перевода строки, и он
  // пуст, когда текст заканчивается переводом (а он заканчивается всегда:
  // строки сюда приходят целыми). Без pop каждый такой хвост добавлял бы
  // лишнюю пустую строку, и вывод расползался вдвое.
  const lines = text.split('\n');
  const tail = lines.pop();

  for (const line of lines) addLine(line);
  if (tail) addLine(tail);

  if (atBottom) $log.scrollTop = $log.scrollHeight;
}

function addLine(line) {
  const el = document.createElement('span');
  const cls = classOf(line);
  if (cls) el.className = cls;
  el.textContent = line + '\n';
  $log.append(el);
}

function setState(text, kind) {
  $state.textContent = text;
  $state.className = 'state' + (kind ? ' ' + kind : '');
}

function busy(on) {
  running = on ? running : null;
  $stop.hidden = !on;
  for (const b of document.querySelectorAll('.tab[data-run]')) b.disabled = on;
  $apply.disabled = on || !planSeen;
}

// Единственное место, где страница ходит на сервер.
async function stream(path, opts) {
  if (running) return;
  const ctrl = new AbortController();
  running = ctrl;
  busy(true);
  setState('идёт…', 'busy');
  clearLog();

  try {
    const res = await fetch(path, {
      method: (opts && opts.method) || 'GET',
      headers: { 'X-Keel-Token': TOKEN },
      signal: ctrl.signal,
    });

    if (!res.ok) {
      append(`✗ ${res.status} ${res.statusText}\n`);
      append(await res.text());
      setState('ошибка', 'fail');
      return false;
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let tail = '';
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      // Чанк может оборваться посреди строки и посреди символа UTF-8 —
      // stream: true собирает символы, а хвост собирает строки
      tail += decoder.decode(value, { stream: true });
      const nl = tail.lastIndexOf('\n');
      if (nl >= 0) {
        append(tail.slice(0, nl + 1));
        tail = tail.slice(nl + 1);
      }
    }
    if (tail) append(tail + '\n');
    setState('готово', 'done');
    return true;
  } catch (e) {
    if (e.name === 'AbortError') {
      append('\n! чтение остановлено — keel на хосте при этом продолжает работу\n');
      setState('прервано', 'fail');
    } else {
      append(`\n✗ связь с keel потеряна: ${e.message}\n`);
      setState('нет связи', 'fail');
    }
    return false;
  } finally {
    busy(false);
  }
}

for (const btn of document.querySelectorAll('.tab[data-run]')) {
  btn.addEventListener('click', async () => {
    for (const b of document.querySelectorAll('.tab[data-run]')) b.classList.remove('active');
    btn.classList.add('active');
    const what = btn.dataset.run;
    const ok = await stream('/api/' + what);
    if (what === 'plan' && ok) {
      planSeen = true;
      $apply.disabled = false;
      $hint.textContent = 'План прочитан. «Применить план» выполнит ровно то, что в нём показано.';
    }
  });
}

// Согласие берётся здесь — это тот самый один вопрос, что и в терминале.
// keel на той стороне запускается с --yes именно потому, что согласие уже
// получено; другого способа его дать у страницы нет.
$apply.addEventListener('click', async () => {
  if (!planSeen) return;
  if (!confirm('Применить показанный план?\n\nkeel изменит хост: пойдёт по модулям сверху вниз и выполнит то, что было в плане.')) return;
  $hint.textContent = 'Идёт применение. Закрывать вкладку можно — keel работает на хосте, а не в браузере.';
  await stream('/api/apply', { method: 'POST' });
  planSeen = false;
  $apply.disabled = true;
  $hint.textContent = 'Применение закончено. Посмотри «Проверка», чтобы сверить хост с манифестом.';
});

$stop.addEventListener('click', () => { if (running) running.abort(); });

// Шапка: чей это хост и какая версия keel
fetch('/api/state', { headers: { 'X-Keel-Token': TOKEN } })
  .then((r) => r.json())
  .then((s) => {
    document.getElementById('host').textContent = s.host || '?';
    document.getElementById('version').textContent = s.version || '';
    document.title = `keel · ${s.host || '?'}`;
  })
  .catch(() => setState('нет связи', 'fail'));
