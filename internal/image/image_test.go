package image

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/vshivtsev-dev/keel/internal/profile"
)

// fakeGitHub изображает и api.github.com, и файловое зеркало: подменяем
// транспорт, чтобы ни один тест не ходил в настоящую сеть.
type fakeGitHub struct {
	release string
	// have — адреса, по которым файл есть. Остальные отвечают 404.
	have map[string]bool
	hits []string
}

func (f *fakeGitHub) RoundTrip(req *http.Request) (*http.Response, error) {
	url := req.URL.String()
	f.hits = append(f.hits, url)

	body := ""
	code := http.StatusNotFound
	switch {
	case strings.HasPrefix(url, "https://api.github.com/"):
		if f.release == "" {
			code = http.StatusServiceUnavailable
		} else {
			code, body = http.StatusOK, f.release
		}
	case f.have[url]:
		code, body = http.StatusPartialContent, "x"
	}
	return &http.Response{
		StatusCode: code,
		Body:       io(body),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func resolverWith(f *fakeGitHub) *Resolver {
	return &Resolver{Client: &http.Client{Transport: f}, cache: map[string]*release{}}
}

const haosRelease = `{
  "tag_name": "18.2",
  "assets": [
    {"name": "haos_ova-18.2.qcow2.xz",
     "browser_download_url": "https://github.com/home-assistant/operating-system/releases/download/18.2/haos_ova-18.2.qcow2.xz"},
    {"name": "haos_generic-x86-64-18.2.img.xz",
     "browser_download_url": "https://github.com/x/generic.img.xz"}
  ]
}`

func haosProfile(t *testing.T) *profile.Profile {
	t.Helper()
	p, err := profile.Load("haos")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Имя файла, пришедшее от самого GitHub, надёжнее собранного нами из
// версии в надежде, что наверху ничего не переименовали.
func TestResolvePrefersAssetFromGitHub(t *testing.T) {
	asset := "https://github.com/home-assistant/operating-system/releases/download/18.2/haos_ova-18.2.qcow2.xz"
	f := &fakeGitHub{release: haosRelease, have: map[string]bool{asset: true}}

	got, err := resolverWith(f).Resolve(context.Background(), haosProfile(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != asset {
		t.Errorf("выбран %s, ожидался файл из ответа GitHub", got.URL)
	}
	if got.Version != "18.2" {
		t.Errorf("версия %q, ожидалась 18.2", got.Version)
	}
	if got.Compressed != "xz" {
		t.Errorf("не распознано сжатие: %q", got.Compressed)
	}
	// Образец должен выбирать ova, а не generic — это разные образы.
	if strings.Contains(got.URL, "generic") {
		t.Errorf("выбран не тот файл релиза: %s", got.URL)
	}
}

// Каждый кандидат проверяется ДО начала закачки: выяснять, что файла нет,
// на середине скачивания полугигабайтного образа — худший из вариантов.
func TestResolveChecksBeforeDownloading(t *testing.T) {
	f := &fakeGitHub{release: haosRelease, have: map[string]bool{}}
	_, err := resolverWith(f).Resolve(context.Background(), haosProfile(t), "")
	if err == nil {
		t.Fatal("несуществующий образ принят за живой")
	}
	var probed bool
	for _, url := range f.hits {
		if strings.Contains(url, "haos_ova") {
			probed = true
		}
	}
	if !probed {
		t.Errorf("адрес не проверялся до закачки: %v", f.hits)
	}
}

// «Не нашёл образ» без списка проверенного не даёт ни одной зацепки.
func TestNotFoundListsTriedURLs(t *testing.T) {
	f := &fakeGitHub{release: haosRelease, have: map[string]bool{}}
	_, err := resolverWith(f).Resolve(context.Background(), haosProfile(t), "")
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Проверено:") || !strings.Contains(msg, "haos_ova") {
		t.Errorf("в ошибке нет списка проверенного:\n%s", msg)
	}
}

// GitHub недоступен — берём запасную версию из профиля и говорим об этом.
func TestResolveFallsBackWhenGitHubSilent(t *testing.T) {
	fallback := "https://github.com/home-assistant/operating-system/releases/download/18.2/haos_ova-18.2.qcow2.xz"
	f := &fakeGitHub{release: "", have: map[string]bool{fallback: true}}

	got, err := resolverWith(f).Resolve(context.Background(), haosProfile(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != fallback {
		t.Errorf("запасной адрес не подобран: %s", got.URL)
	}
}

// Версия, закреплённая в манифесте, важнее всего остального: человек
// закрепил её осознанно.
func TestResolveRespectsPinnedVersion(t *testing.T) {
	pinned := "https://github.com/home-assistant/operating-system/releases/download/17.0/haos_ova-17.0.qcow2.xz"
	f := &fakeGitHub{release: haosRelease, have: map[string]bool{pinned: true,
		"https://github.com/home-assistant/operating-system/releases/download/18.2/haos_ova-18.2.qcow2.xz": true}}

	got, err := resolverWith(f).Resolve(context.Background(), haosProfile(t), "17.0")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "17.0" {
		t.Errorf("закреплённая версия проигнорирована: %s → %s", got.Version, got.URL)
	}
}

// У облачного образа адрес в профиле прямой — спрашивать некого.
func TestResolveUsesDirectURLWithoutNetwork(t *testing.T) {
	p, err := profile.Load("debian-cloud")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeGitHub{}
	got, err := resolverWith(f).Resolve(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.URL, "debian-13-genericcloud") {
		t.Errorf("прямая ссылка не взята: %s", got.URL)
	}
	if len(f.hits) != 0 {
		t.Errorf("при прямой ссылке keel всё же полез в сеть: %v", f.hits)
	}
}

func TestLocalName(t *testing.T) {
	cases := []struct{ url, comp, archive, image string }{
		{"https://x/haos_ova-18.2.qcow2.xz", "xz", "haos_ova-18.2.qcow2.xz", "haos_ova-18.2.qcow2"},
		{"https://x/debian.qcow2", "none", "debian.qcow2", "debian.qcow2"},
		{"https://x/img.raw.gz", "gz", "img.raw.gz", "img.raw"},
	}
	for _, c := range cases {
		a, i := LocalName(c.url, c.comp)
		if a != c.archive || i != c.image {
			t.Errorf("LocalName(%q,%q) = %q,%q; ожидалось %q,%q", c.url, c.comp, a, i, c.archive, c.image)
		}
	}
}
