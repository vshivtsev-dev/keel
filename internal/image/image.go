// Package image находит, откуда скачать образ гостя.
//
// Главное правило здесь: каждый кандидат проверяется до начала закачки.
// 404 от GitHub — это определённый ответ «такого файла нет», а не сбой
// связи: повторять бессмысленно, надо брать другой адрес. Выяснять это
// на середине скачивания образа в полгигабайта — худший из вариантов.
package image

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/vshivtsev-dev/keel/internal/profile"
)

// Resolver спрашивает у сети, что и откуда качать.
type Resolver struct {
	Client *http.Client
	// cache хранит ответ GitHub про релиз: за один запуск спрашиваем один
	// раз, дальше из того же ответа берётся и версия, и имя файла.
	cache map[string]*release
}

func NewResolver() *Resolver {
	return &Resolver{
		Client: &http.Client{Timeout: 20 * time.Second},
		cache:  map[string]*release{},
	}
}

type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Resolved — где лежит образ и что с ним делать после закачки.
type Resolved struct {
	URL string
	// Version — версия, которую keel в итоге выбрал. Показывается человеку:
	// «latest» в манифесте и «18.2» на деле — разные вещи, и знать, что
	// именно приедет, он должен до закачки.
	Version string
	// Compressed — чем распаковывать: xz, gz или пусто.
	Compressed string
	// Fallback — взята запасная версия, потому что нужной по ожидаемому
	// адресу не нашлось.
	Fallback bool
	// Tried — адреса, которые проверили и отвергли. Нужны в сообщении об
	// ошибке: «не нашёл образ» без списка проверенного бесполезно.
	Tried []string
}

// Resolve ищет живой адрес образа.
//
// Порядок нарочно такой:
//  1. явная ссылка из профиля — её не оспариваем;
//  2. имя файла из ответа GitHub — надёжнее шаблона, потому что приходит
//     от самого GitHub, а не собирается нами в надежде, что наверху ничего
//     не переименовали;
//  3. шаблон с найденной версией;
//  4. шаблон с запасной версией из профиля.
func (r *Resolver) Resolve(ctx context.Context, p *profile.Profile, wantVersion string) (*Resolved, error) {
	img := p.Image
	if img == nil {
		return nil, fmt.Errorf("профиль %s: образ не описан", p.Name)
	}
	out := &Resolved{Compressed: img.Compressed}

	if img.URL != "" {
		out.URL, out.Version = img.URL, img.Version
		return out, nil
	}

	version := r.version(ctx, img, wantVersion)
	out.Version = version

	if img.AssetPattern != "" && img.GitHubRepo != "" {
		if url := r.asset(ctx, img.GitHubRepo, img.AssetPattern); url != "" {
			if r.exists(ctx, url) {
				out.URL = url
				return out, nil
			}
			out.Tried = append(out.Tried, url)
		}
	}

	if url := urlFor(img.URLTemplate, version); url != "" {
		if r.exists(ctx, url) {
			out.URL = url
			return out, nil
		}
		out.Tried = append(out.Tried, url)
	}

	if fb := img.FallbackVersion; fb != "" && fb != version {
		if url := urlFor(img.URLTemplate, fb); url != "" {
			if r.exists(ctx, url) {
				out.URL, out.Version, out.Fallback = url, fb, true
				return out, nil
			}
			out.Tried = append(out.Tried, url)
		}
	}

	return out, &NotFoundError{Profile: p.Name, Tried: out.Tried}
}

// NotFoundError перечисляет проверенное: «не нашёл образ» без списка
// адресов не даёт человеку ни одной зацепки.
type NotFoundError struct {
	Profile string
	Tried   []string
}

func (e *NotFoundError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "профиль %s: не нашёл ни одного живого адреса образа", e.Profile)
	if len(e.Tried) == 0 {
		b.WriteString(" (в профиле нет ни ссылки, ни шаблона)")
		return b.String()
	}
	b.WriteString(". Проверено:")
	for _, t := range e.Tried {
		b.WriteString("\n    " + t)
	}
	return b.String()
}

// version: сначала то, что попросили в манифесте, потом профиль, потом
// последний релиз с GitHub, потом запасная из профиля.
func (r *Resolver) version(ctx context.Context, img *profile.Image, want string) string {
	if want != "" {
		return want
	}
	if img.Version != "" && img.Version != "latest" {
		return img.Version
	}
	if img.GitHubRepo != "" {
		if rel := r.release(ctx, img.GitHubRepo); rel != nil && rel.TagName != "" {
			return rel.TagName
		}
	}
	return img.FallbackVersion
}

func (r *Resolver) asset(ctx context.Context, repo, pattern string) string {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return ""
	}
	rel := r.release(ctx, repo)
	if rel == nil {
		return ""
	}
	for _, a := range rel.Assets {
		if re.MatchString(a.Name) {
			return a.URL
		}
	}
	return ""
}

func (r *Resolver) release(ctx context.Context, repo string) *release {
	if r.cache == nil {
		r.cache = map[string]*release{}
	}
	if rel, ok := r.cache[repo]; ok {
		return rel
	}
	r.cache[repo] = nil

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := r.client().Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil
	}
	r.cache[repo] = &rel
	return &rel
}

// exists тянет один байт, а не шлёт HEAD: часть зеркал и CDN на HEAD
// отвечают отказом, и «нет ответа» стало бы неотличимо от «нет файла».
func (r *Resolver) exists(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Range", "bytes=0-0")

	resp, err := r.client().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 400
}

func (r *Resolver) client() *http.Client {
	if r.Client == nil {
		r.Client = &http.Client{Timeout: 20 * time.Second}
	}
	return r.Client
}

func urlFor(template, version string) string {
	if template == "" || version == "" {
		return ""
	}
	return strings.ReplaceAll(template, "${version}", version)
}

// LocalName — имя файла образа в кэше и имя после распаковки.
func LocalName(url, compressed string) (archive, image string) {
	archive = url[strings.LastIndexByte(url, '/')+1:]
	image = archive
	switch compressed {
	case "xz":
		image = strings.TrimSuffix(archive, ".xz")
	case "gz":
		image = strings.TrimSuffix(archive, ".gz")
	}
	return archive, image
}
