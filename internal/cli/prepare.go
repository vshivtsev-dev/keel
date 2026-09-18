package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/profile"
)

// Prepare задаёт все вопросы до того, как план собран.
//
// Это переворот по сравнению с bash-версией: там пароль рабочего стола и
// токен туннеля спрашивались посреди применения. С планом-артефактом так
// нельзя — план должен быть исполним без человека, иначе он не артефакт,
// а обещание.
//
// Ответы кладутся в secrets/ с правами 0600, а в план идёт метка. Ключи
// SSH читаются здесь же: провайдер в файловую систему не ходит.
func (a *App) Prepare(m *manifest.Manifest) error {
	for i := range m.Guests {
		g := &m.Guests[i]

		name, err := profile.Resolve(g.Profile, g.Graphics)
		if err != nil || name == "" {
			continue // о непонятном профиле скажет сам провайдер
		}
		p, err := profile.Load(name)
		if err != nil {
			continue
		}

		if err := a.fillSSHKeys(g); err != nil {
			return err
		}
		if p.NeedsPassword {
			if err := a.ensurePassword(g); err != nil {
				return err
			}
		}
		if p.NeedsToken {
			if err := a.ensureToken(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensurePassword спрашивает пароль для входа в гостя. В манифесте паролей
// нет и не будет: они живут отдельными файлами, и посмотреть их потом
// можно командой keel password.
func (a *App) ensurePassword(g *manifest.Guest) error {
	ref := strconv.Itoa(g.ID)
	if a.Secrets.Has(ref) {
		value, err := a.Secrets.Get(ref)
		if err != nil {
			return err
		}
		a.Masker.Add(value)
		return nil
	}

	value, err := a.Secrets.Ensure(ref, func() (string, error) {
		fmt.Fprintf(a.Out, "\nПароль для входа в %d «%s».\n", g.ID, g.Name)
		fmt.Fprintf(a.Out, "Пусто — придумаю сам; посмотреть потом: keel password %d\n", g.ID)
		return a.askHidden("Пароль: ")
	})
	if err != nil {
		return err
	}
	a.Masker.Add(value)
	fmt.Fprintf(a.Out, "Пароль сохранён: %s\n", filepath.Join(a.Paths.Secrets(), ref+".txt"))
	return nil
}

// ensureToken спрашивает токен внешней службы. Придумать его keel не
// может — без него гость бессмысленен.
func (a *App) ensureToken(p *profile.Profile) error {
	ref := p.TokenFile
	if ref == "" {
		ref = p.Name
	}
	if a.Secrets.Has(ref) {
		value, err := a.Secrets.Get(ref)
		if err != nil {
			return err
		}
		a.Masker.Add(value)
		return nil
	}

	if p.TokenHint != "" {
		fmt.Fprintf(a.Out, "\n%s\n", p.TokenHint)
	}
	value, err := a.askHidden("Токен: ")
	if err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("токен для %s не задан, а придумать его keel не может.\n"+
			"Положи его одной строкой в %s", p.Name, filepath.Join(a.Paths.Secrets(), ref+".txt"))
	}
	if err := a.Secrets.Put(ref, strings.TrimSpace(value)); err != nil {
		return err
	}
	a.Masker.Add(strings.TrimSpace(value))
	return nil
}

// fillSSHKeys читает открытые ключи для гостя: из файла, указанного в
// манифесте, либо с самого хоста.
func (a *App) fillSSHKeys(g *manifest.Guest) error {
	if g.CloudInit == nil {
		return nil
	}
	if file := g.CloudInit.SSHKeyFile; file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("гость %d: нет файла с ключами %s", g.ID, file)
		}
		g.CloudInit.SSHKeys = splitKeys(string(raw))
		return nil
	}
	if g.CloudInit.SSHKeyFrom != "host" {
		return nil
	}

	var keys []string
	home, _ := os.UserHomeDir()
	var candidates []string
	for _, dir := range []string{home, "/root"} {
		if dir == "" {
			continue
		}
		candidates = append(candidates, filepath.Join(dir, ".ssh", "authorized_keys"))
		pub, _ := filepath.Glob(filepath.Join(dir, ".ssh", "id_*.pub"))
		candidates = append(candidates, pub...)
	}
	sort.Strings(candidates)

	seen := map[string]bool{}
	for _, path := range candidates {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, k := range splitKeys(string(raw)) {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		fmt.Fprintf(a.Out, "! На хосте не нашлось открытых ключей — гость %d останется без ключа.\n", g.ID)
	}
	g.CloudInit.SSHKeys = keys
	return nil
}

func splitKeys(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// askHidden читает ответ у терминала. Спрашивать надо у человека, даже
// когда вывод перенаправлен, поэтому идём в /dev/tty напрямую.
func (a *App) askHidden(prompt string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("спросить некого: нет терминала.\n"+
			"Положи значение в %s одной строкой", a.Paths.Secrets())
	}
	defer tty.Close()

	fmt.Fprint(tty, prompt)
	line, err := bufio.NewReader(tty).ReadString('\n')
	fmt.Fprintln(tty)
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// checkGuestSelection ловит опечатку в номере гостя.
//
// Без этой проверки «--guest 1O1» выглядит как «делать нечего»: провайдер
// молча никого не выберет, план окажется пустым, и человек решит, что всё
// уже настроено.
func (a *App) checkGuestSelection(m *manifest.Manifest) error {
	if len(a.Opts.Guests) == 0 {
		return nil
	}
	have := map[int]bool{}
	var known []string
	for _, g := range m.Guests {
		have[g.ID] = true
		known = append(known, strconv.Itoa(g.ID))
	}

	var missing []string
	for id := range a.Opts.Guests {
		if !have[id] {
			missing = append(missing, strconv.Itoa(id))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	list := "ни одного"
	if len(known) > 0 {
		sort.Strings(known)
		list = strings.Join(known, ", ")
	}
	return fmt.Errorf("в манифесте нет %s: %s.\nЕсть: %s",
		plural(len(missing), "гостя", "гостей", "гостей"), strings.Join(missing, ", "), list)
}
