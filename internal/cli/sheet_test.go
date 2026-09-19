package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// /dev/null — символьное устройство, и самодельная проверка «символьное
// устройство значит терминал» считала его экраном. Из-за этого `keel`
// без терминала лез в отрисовку и падал внутренней ошибкой библиотеки
// вместо внятного объяснения, а `keel doctor > /dev/null` красил вывод
// в никуда.
func TestNullDeviceIsNotATerminal(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("нет %s: %v", os.DevNull, err)
	}
	defer null.Close()

	if colorEnabled(null) {
		t.Errorf("%s принят за экран — вывод будет раскрашен в никуда", os.DevNull)
	}
}

func TestPipeAndFileAreNotTerminals(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if colorEnabled(w) {
		t.Error("труба принята за экран")
	}

	file, err := os.Create(filepath.Join(t.TempDir(), "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if colorEnabled(file) {
		t.Error("обычный файл принят за экран")
	}

	// И то, что вообще не файл.
	if colorEnabled(&bytes.Buffer{}) {
		t.Error("буфер в памяти принят за экран")
	}
}

// NO_COLOR — общепринятый способ сказать «не надо», и его надо слушать
// даже на настоящем экране.
func TestNoColorIsRespected(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		t.Skip("терминала нет — проверять не на чем")
	}
	defer tty.Close()
	if colorEnabled(tty) {
		t.Error("NO_COLOR проигнорирован")
	}
}

// Без цвета вывод должен остаться читаемым текстом, а не потерять
// разметку вместе с управляющими последовательностями.
func TestSheetWithoutColorStaysReadable(t *testing.T) {
	out := &bytes.Buffer{}
	s := newSheet(out)
	s.section("Хост")
	s.ok("Proxmox VE", "9.0.3")
	s.warn("IOMMU", "выключен")
	s.row("хранилище", "local")

	got := out.String()
	if strings.Contains(got, "\033[") {
		t.Errorf("в небуферном выводе есть управляющие последовательности:\n%q", got)
	}
	for _, want := range []string{"Хост", "Proxmox VE", "9.0.3", "IOMMU", "выключен", "local"} {
		if !strings.Contains(got, want) {
			t.Errorf("потеряно %q:\n%s", want, got)
		}
	}
}
