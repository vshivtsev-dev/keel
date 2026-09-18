// Package netcheck меряет связь до репозитория перед закачкой.
//
// Однажды apt на домашнем хосте дорос до 6,4 ГБ и едва не увёл машину в
// OOM. Причина была не в apt: DNS отдавал адреса IPv6, маршрута до них не
// было, а IPv4 отдавал 4 КБ/с. Очередь закачек на сотню пакетов раз за
// разом откладывала и переставляла элементы, каждый со своим состоянием, —
// отсюда и гигабайты.
//
// Поэтому связь проверяется, а не предполагается.
package netcheck

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Probe — сколько качать и сколько ждать.
type Probe struct {
	// Limit держит пробу в разумных пределах: мерить скорость, выкачивая
	// пакет целиком, невежливо по отношению к зеркалу.
	Limit int64
	// Timeout — сколько ждать. Дольше нет смысла: на умирающей сети это
	// и есть ответ.
	Timeout time.Duration
	Client  *http.Client
}

func Default() Probe {
	return Probe{Limit: 4 << 20, Timeout: 15 * time.Second}
}

// Speed измеряет скорость закачки по ссылке в байтах в секунду.
// Ноль означает «не достучались», и это не то же самое, что «медленно».
func (p Probe) Speed(ctx context.Context, url string) (int64, error) {
	if p.Limit <= 0 {
		p.Limit = 4 << 20
	}
	if p.Timeout <= 0 {
		p.Timeout = 15 * time.Second
	}
	client := p.Client
	if client == nil {
		client = &http.Client{}
	}

	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	// Диапазон, а не HEAD: часть зеркал и CDN на HEAD отвечают отказом, и
	// «нет ответа» стало бы неотличимо от «нет файла».
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", p.Limit-1))

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("репозиторий ответил %s", resp.Status)
	}

	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, p.Limit))
	elapsed := time.Since(start)
	if err != nil && n == 0 {
		return 0, err
	}
	if elapsed <= 0 {
		return 0, nil
	}
	return int64(float64(n) / elapsed.Seconds()), nil
}
