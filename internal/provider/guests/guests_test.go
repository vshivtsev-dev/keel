package guests

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/image"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

// alwaysThere изображает сеть, в которой любой образ на месте: тесты
// проверяют сборку команд, а не доступность зеркал.
type alwaysThere struct{}

func (alwaysThere) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusPartialContent,
		Body: http.NoBody, Header: http.Header{}, Request: req}, nil
}

func provider(only ...int) Guests {
	g := Guests{
		Images:   &image.Resolver{Client: &http.Client{Transport: alwaysThere{}}},
		CacheDir: "/root/keel/images",
	}
	if len(only) > 0 {
		g.Only = map[int]bool{}
		for _, id := range only {
			g.Only[id] = true
		}
	}
	return g
}

// guestHost — подставной хост: есть хранилище со сниппетами, шаблон
// контейнера скачан, видеокарта на месте.
func guestHost() *facts.Facts {
	return &facts.Facts{
		Hostname: "pve-01",
		Storages: []facts.Storage{
			{Name: "local", Type: "dir", Path: "/var/lib/vz",
				Content: []string{"backup", "iso", "snippets", "vztmpl"}},
			{Name: "local-lvm", Type: "lvmthin", Content: []string{"images", "rootdir"}},
		},
		TemplatesDownloaded: []string{
			"ubuntu-24.04-standard_24.04-2_amd64.tar.zst",
			"debian-13-standard_13.0-1_amd64.tar.zst",
		},
		DRINodes:   []string{"/dev/dri/card0", "/dev/dri/renderD128"},
		PctDevKeys: true,
	}
}

func parse(t *testing.T, body string) *manifest.Manifest {
	t.Helper()
	m, err := manifest.Parse([]byte(body))
	if err != nil {
		t.Fatalf("манифест не разобрался: %v", err)
	}
	return m
}

func planOf(t *testing.T, g Guests, body string, f *facts.Facts) plan.Changes {
	t.Helper()
	c, err := g.Plan(context.Background(), parse(t, body), f)
	if err != nil {
		t.Fatalf("сборка плана: %v", err)
	}
	return c
}

func commands(steps []plan.Step) []string {
	var out []string
	for _, s := range steps {
		if s.Action == plan.ActionExec {
			out = append(out, exec.Render(s.Cmd))
		}
	}
	return out
}

func ranWith(t *testing.T, c plan.Changes, want string) {
	t.Helper()
	for _, cmd := range commands(c.Steps) {
		if strings.Contains(cmd, want) {
			return
		}
	}
	t.Errorf("нет команды с %q; есть:\n  %s", want, strings.Join(commands(c.Steps), "\n  "))
}

func notRan(t *testing.T, c plan.Changes, unwanted string) {
	t.Helper()
	for _, cmd := range commands(c.Steps) {
		if strings.Contains(cmd, unwanted) {
			t.Errorf("появилась команда с %q: %s", unwanted, cmd)
		}
	}
}

// --- Виртуальная машина из готового образа -----------------------------------

func TestVMFromImageCommands(t *testing.T) {
	c := planOf(t, provider(), `{"guests":[{
		"id":100,"name":"haos","profile":"haos",
		"storage":"local-lvm","disk":"32G","start_on_boot":true}]}`, guestHost())

	for _, want := range []string{
		"qm create 100 --name haos",
		"--machine q35",
		"--bios ovmf",
		"--efidisk0 local-lvm:0,efitype=4m,pre-enrolled-keys=0",
		"import-from=/root/keel/images/haos_ova-18.2.qcow2",
		"qm set 100 --boot order=scsi0",
		"qm resize 100 scsi0 32G",
		"--onboot 1",
	} {
		ranWith(t, c, want)
	}
	// Образ сжат xz — его надо распаковать до импорта.
	ranWith(t, c, "xz -d -k")
}

// --- Виртуальная машина из облачного образа ----------------------------------

func TestCloudInitVMCommands(t *testing.T) {
	c := planOf(t, provider(), `{"guests":[{
		"id":101,"name":"desktop","profile":"desktop","graphics":"virgl",
		"storage":"local-lvm","disk":"64G",
		"cloudinit":{"user":"av","ipconfig":"ip=dhcp"},
		"packages":["obs-studio"]}]}`, guestHost())

	for _, want := range []string{
		"qm create 101 --name desktop",
		"--vga virtio-gl",
		"--audio0 device=ich9-intel-hda,driver=spice",
		"qm set 101 --ide2 local-lvm:cloudinit",
		"qm set 101 --cicustom user=local:snippets/keel-101-user.yml",
		"qm set 101 --ipconfig0 ip=dhcp",
	} {
		ranWith(t, c, want)
	}

	// Сам файл настроек первого запуска.
	var snippet plan.Step
	for _, s := range c.Steps {
		if s.Action == plan.ActionWrite && strings.HasSuffix(s.Path, "keel-101-user.yml") {
			snippet = s
		}
	}
	if snippet.ID == "" {
		t.Fatalf("настройки первого запуска не записываются: %+v", c.Steps)
	}
	body := string(snippet.Content)
	for _, want := range []string{
		"#cloud-config",
		"name: av",
		"- vlc",        // из профиля
		"- obs-studio", // из манифеста
		"systemctl set-default graphical.target",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("в настройках первого запуска нет %q:\n%s", want, body)
		}
	}
	if snippet.Path != "/var/lib/vz/snippets/keel-101-user.yml" {
		t.Errorf("сниппет кладётся не туда: %s", snippet.Path)
	}
}

// Без сниппетов cloud-init не настроить, и делать вид, что ВМ создастся
// правильно, нельзя.
func TestCloudInitRefusesWithoutSnippetStorage(t *testing.T) {
	f := guestHost()
	f.Storages[0].Content = []string{"backup", "iso", "vztmpl"}

	_, err := provider().Plan(context.Background(),
		parse(t, `{"guests":[{"id":101,"name":"d","profile":"desktop","graphics":"virgl"}]}`), f)
	if err == nil {
		t.Fatal("ВМ с cloud-init собралась без хранилища для сниппетов")
	}
	if !strings.Contains(err.Error(), "сниппет") || !strings.Contains(err.Error(), "host/storage") {
		t.Errorf("ошибка не подсказывает, чем чинить: %v", err)
	}
}

// --- Контейнер ---------------------------------------------------------------

func TestLXCDesktopCommands(t *testing.T) {
	c := planOf(t, provider(), `{"guests":[{
		"id":102,"name":"desktop","profile":"desktop","graphics":"dri",
		"storage":"local-lvm","disk":"64G","cores":4,"memory":8192,
		"cloudinit":{"user":"av"}}]}`, guestHost())

	for _, want := range []string{
		"pct create 102 local:vztmpl/ubuntu-24.04-standard_24.04-2_amd64.tar.zst",
		"--rootfs local-lvm:64",
		"--unprivileged 1",
		"--features nesting=1",
		"renderD128,gid=993",
		"card0,gid=44",
		"pct start 102",
		"pct exec 102 -- bash /root/keel-post-install.sh",
	} {
		ranWith(t, c, want)
	}
	// Шаблон уже скачан — качать его заново незачем.
	notRan(t, c, "pveam download")
}

func TestLXCDownloadsMissingTemplate(t *testing.T) {
	f := guestHost()
	f.TemplatesDownloaded = nil
	f.TemplatesAvailable = []string{"ubuntu-24.04-standard_24.04-2_amd64.tar.zst"}

	c := planOf(t, provider(), `{"guests":[{"id":102,"name":"d","profile":"desktop","graphics":"dri"}]}`, f)
	ranWith(t, c, "pveam update")
	ranWith(t, c, "pveam download local ubuntu-24.04-standard_24.04-2_amd64.tar.zst")
}

// Ключи dev0 появились в PVE 8.2. Делать вид, что команда сработает на
// более старом хосте, нельзя.
func TestLXCRefusesDRIWithoutDevKeys(t *testing.T) {
	f := guestHost()
	f.PctDevKeys = false

	_, err := provider().Plan(context.Background(),
		parse(t, `{"guests":[{"id":102,"name":"d","profile":"desktop","graphics":"dri"}]}`), f)
	if err == nil {
		t.Fatal("видеокарта отдана контейнеру на хосте без поддержки dev0")
	}
	if !strings.Contains(err.Error(), "8.2") {
		t.Errorf("ошибка не называет нужную версию: %v", err)
	}
}

// --- Секреты -----------------------------------------------------------------

// Пароль попадает в сценарий настройки контейнера, а план сохраняется
// файлом на диск. Значит в плане должна стоять метка, а не пароль.
func TestPasswordNeverLandsInPlan(t *testing.T) {
	c := planOf(t, provider(), `{"guests":[{
		"id":102,"name":"d","profile":"desktop","graphics":"dri",
		"cloudinit":{"user":"av"}}]}`, guestHost())

	var push plan.Step
	for _, s := range c.Steps {
		if strings.HasSuffix(s.ID, ":push") {
			push = s
		}
	}
	if push.ID == "" {
		t.Fatalf("сценарий настройки не загружается: %+v", c.Steps)
	}
	body := strings.Join(push.Cmd, " ")
	if !strings.Contains(body, "@@keel-secret:102@@") {
		t.Errorf("в сценарии нет метки секрета:\n%s", body)
	}
	if len(push.SecretRefs) != 1 || push.SecretRefs[0] != "102" {
		t.Errorf("шаг не объявляет, какой секрет ему нужен: %v", push.SecretRefs)
	}
	// Сценарий уносит с собой пароль и остаётся лежать в контейнере,
	// поэтому последней строкой удаляет сам себя.
	if !strings.Contains(body, `rm -f "$0"`) {
		t.Errorf("сценарий не удаляет сам себя:\n%s", body)
	}
}

// Токен туннеля keel придумать не может, а команду с ним знает профиль.
func TestTokenSubstitutedByProfile(t *testing.T) {
	c := planOf(t, provider(), `{"guests":[{"id":103,"name":"tunnel","profile":"cloudflared"}]}`, guestHost())

	var push plan.Step
	for _, s := range c.Steps {
		if strings.HasSuffix(s.ID, ":push") {
			push = s
		}
	}
	body := strings.Join(push.Cmd, " ")
	if !strings.Contains(body, "cloudflared service install @@keel-secret:cloudflared@@") {
		t.Errorf("команда установки токена собрана неверно:\n%s", body)
	}
	// Служебному контейнеру пользователь не нужен: лишняя учётная запись
	// с паролем — это лишняя учётная запись с паролем.
	if strings.Contains(body, "adduser --disabled-password") {
		t.Errorf("в служебном контейнере заведён пользователь:\n%s", body)
	}
}

// --- Правила -----------------------------------------------------------------

// Виртуалка с данными дороже любой стройности.
func TestExistingGuestIsNeverTouched(t *testing.T) {
	f := guestHost()
	f.Guests = []facts.Guest{{ID: 100, Kind: "vm", Name: "haos"}}

	c := planOf(t, provider(), `{"guests":[{"id":100,"name":"haos","profile":"haos"}]}`, f)
	if len(c.Steps) != 0 {
		t.Fatalf("существующий гость всё же трогается: %v", commands(c.Steps))
	}
	if len(c.Notes) != 1 || !strings.Contains(c.Notes[0].Message, "не трогает") {
		t.Errorf("о существующем госте не сказано: %+v", c.Notes)
	}
}

// Сужение выбора нельзя скрывать: иначе «делать нечего» читается как
// «все гости на месте», хотя половину мы даже не смотрели.
func TestNarrowedSelectionIsAnnounced(t *testing.T) {
	c := planOf(t, provider(100), `{"guests":[
		{"id":100,"name":"haos","profile":"haos"},
		{"id":101,"name":"d","profile":"desktop","graphics":"virgl"}]}`, guestHost())

	notRan(t, c, "qm create 101")
	ranWith(t, c, "qm create 100")

	var told bool
	for _, n := range c.Notes {
		if strings.Contains(n.Message, "пропущено по выбору") {
			told = true
		}
	}
	if !told {
		t.Errorf("о сужении выбора не сказано: %+v", c.Notes)
	}
}

func TestUnmanagedGuestIsReportedNotTouched(t *testing.T) {
	f := guestHost()
	f.Guests = []facts.Guest{{ID: 300, Kind: "lxc", Name: "чужой"}}

	findings, err := provider().Verify(context.Background(), parse(t, `{"guests":[]}`), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || !findings[0].Unmanaged || !findings[0].OK {
		t.Fatalf("гость вне манифеста помечен неверно: %+v", findings)
	}
}

func TestRuleOfZero(t *testing.T) {
	if (Guests{}).Configured(parse(t, `{}`)) {
		t.Error("без гостей в манифесте провайдер объявил себя настроенным")
	}
}

func TestDiskToGB(t *testing.T) {
	cases := map[string]int{"64G": 64, "64": 64, "65536M": 64, "1T": 1024, "": 0, "мусор": 0}
	for in, want := range cases {
		if got := diskToGB(in); got != want {
			t.Errorf("diskToGB(%q) = %d, ожидалось %d", in, got, want)
		}
	}
}
