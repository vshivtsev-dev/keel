package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/vshivtsev-dev/keel/internal/ru"
)

// wideFrom — ширина, начиная с которой помещаются три зоны.
//
// Ниже неё экран раскладывается в одну колонку. Это не «на всякий случай»:
// консоль Proxmox по IPMI и терминал на телефоне — ровно те места, где
// хост и чинят, когда всё сломалось.
const wideFrom = 100

func (m *Model) View() string {
	if m.width < 40 || m.height < 10 {
		return "Окно слишком мало. Растяни его хотя бы до 40×10."
	}
	view := m.viewCompact()
	switch {
	case m.help:
		view = m.viewHelp()
	case m.wide():
		view = m.viewWide()
	}
	// Страховка на весь кадр. Ширину зон keel считает сам, но у рамок и
	// значков своя арифметика, и разъехавшаяся на одну колонку рамка
	// читается как сломанная программа. Дешевле обрезать, чем сломать.
	return lipgloss.NewStyle().MaxWidth(m.width).MaxHeight(m.height).Render(view)
}

func (m *Model) wide() bool { return m.width >= wideFrom }

// --- широкая раскладка -------------------------------------------------------

func (m *Model) viewWide() string {
	head := m.header()
	foot := m.footer()
	body := m.height - lipgloss.Height(head) - lipgloss.Height(foot)
	if body < 6 {
		body = 6
	}

	// Ширины считаются в полных колонках, включая рамку: рамка занимает
	// по одной колонке с каждой стороны, поэтому содержимому остаётся
	// на две меньше.
	leftTotal := m.width * 3 / 5
	rightTotal := m.width - leftTotal
	leftInner, rightInner := leftTotal-2, rightTotal-2

	// То же по высоте: рамка занимает две строки, заголовок зоны — ещё
	// одну внутри неё. Обе колонки должны получиться ровно в body строк,
	// иначе одна окажется длиннее другой и рамки разъедутся.
	paneH := body - 2
	hostH := body/2 - 2
	lowerH := body - hostH - 4

	tree := m.theme.pane("Что делаем", m.treeBody(leftInner, paneH-1), leftInner, paneH,
		m.focus == zoneTree)

	// Правая колонка: факты о хосте сверху, сводка или ход дела снизу.
	hostPane := m.theme.pane("Хост", m.hostBody(rightInner), rightInner, hostH, false)
	lowerTitle, lowerText := m.lowerPane(rightInner)
	lowerPane := m.theme.pane(lowerTitle, lowerText, rightInner, lowerH, m.focus == zoneDetails)

	right := lipgloss.JoinVertical(lipgloss.Left, hostPane, lowerPane)
	return lipgloss.JoinVertical(lipgloss.Left, head,
		lipgloss.JoinHorizontal(lipgloss.Top, tree, right), foot)
}

// --- аварийная раскладка -----------------------------------------------------

// viewCompact — одна колонка. Зоны никуда не деваются: они становятся
// секциями, между которыми ходит Tab.
func (m *Model) viewCompact() string {
	head := m.compactHeader()
	foot := m.footer()
	body := m.height - lipgloss.Height(head) - lipgloss.Height(foot) - 2
	if body < 4 {
		body = 4
	}

	inner := m.width - 2
	var pane string
	switch {
	case m.focus == zoneDetails || m.stage == stageRunning || m.stage == stageDone:
		title, text := m.lowerPane(inner)
		pane = m.theme.pane(title, text, inner, body, true)
	default:
		pane = m.theme.pane("Что делаем", m.treeBody(inner, body), inner, body, true)
	}
	return lipgloss.JoinVertical(lipgloss.Left, head, pane, foot)
}

// --- части экрана ------------------------------------------------------------

func (m *Model) header() string {
	host := "?"
	if m.facts != nil && m.facts.Hostname != "" {
		host = m.facts.Hostname
	}
	path := ""
	if m.manifest != nil {
		path = m.manifest.Path
	}
	line := fmt.Sprintf(" keel %s · %s · %s", m.app.Version, host, path)
	if m.app.Paths.Sandboxed() {
		line += " · " + m.theme.warn.Render("песочница")
	}
	right := m.theme.dim.Render("[?] помощь ")
	gap := m.width - lipgloss.Width(line) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return m.theme.header.Render(line) + strings.Repeat(" ", gap) + right
}

// compactHeader вмещает то же самое в одну узкую строку: на телефоне
// путь к манифесту не нужен, а имя хоста — очень.
func (m *Model) compactHeader() string {
	host := "?"
	if m.facts != nil && m.facts.Hostname != "" {
		host = m.facts.Hostname
	}
	facts := ""
	if m.facts != nil {
		facts = " · " + m.shortFacts()
	}
	return m.theme.header.Render(truncate(" keel · "+host+facts, m.width))
}

func (m *Model) shortFacts() string {
	parts := []string{}
	if m.facts.PVEVersion != "" {
		parts = append(parts, shortPVE(m.facts.PVEVersion))
	}
	if m.facts.Upgradable > 0 {
		parts = append(parts, ru.Package(m.facts.Upgradable))
	}
	return strings.Join(parts, " · ")
}

func (m *Model) footer() string {
	if m.status != "" {
		return m.theme.warn.Render(" " + truncate(m.status, m.width-2))
	}
	var hints string
	switch m.stage {
	case stageLoading:
		hints = "собираю состояние хоста…"
	case stageChoosing:
		hints = "[␣] отметить  [enter] подробнее  [a] применить  [p] пересчитать  [q] выход"
		if !m.wide() {
			hints = "[␣] выбор [a] применить [tab] панель [q] выход"
		}
	case stageConfirm:
		hints = "[enter] применить  [n] вернуться"
	case stageRunning:
		hints = "идёт применение…"
	case stageDone:
		hints = "[p] пересчитать  [q] выход"
	case stageError:
		hints = "[p] попробовать снова  [q] выход"
	}
	return m.theme.footer.Render(" " + truncate(hints, m.width-2))
}

func (m *Model) treeBody(width, height int) string {
	if m.stage == stageLoading {
		return m.theme.dim.Render("…")
	}
	if len(m.rows) == 0 {
		return m.theme.dim.Render("в манифесте ничего не описано —\nkeel не тронет ничего")
	}

	// Прокрутка: курсор всегда в окне, но сам список не прыгает.
	start := 0
	if m.cursor >= height {
		start = m.cursor - height + 1
	}
	end := start + height
	if end > len(m.rows) {
		end = len(m.rows)
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		b.WriteString(m.renderRow(m.rows[i], i, width))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) hostBody(width int) string {
	if m.facts == nil {
		return m.theme.dim.Render("…")
	}
	f := m.facts
	var lines []string
	add := func(k, v string) {
		if v != "" {
			lines = append(lines, truncate(m.theme.dim.Render(k+" ")+v, width))
		}
	}
	add("PVE", shortPVE(f.PVEVersion))
	add("процессор", f.CPUModel)
	if f.IOMMU {
		add("IOMMU", m.theme.ok.Render("включён"))
	} else {
		add("IOMMU", m.theme.dim.Render("выключен"))
	}
	for _, g := range f.GPUs {
		add("видео", g.Desc+" ("+g.Driver+")")
	}
	for _, s := range f.Storages {
		add("хранилище", s.Name+" "+s.Type)
	}
	if f.Upgradable > 0 {
		add("обновления", ru.Package(f.Upgradable))
	}
	if f.RebootRequired {
		add("перезагрузка", m.theme.warn.Render("система просит"))
	}
	return strings.Join(lines, "\n")
}

// lowerPane — то, что меняется по ходу задачи: сводка выбранного,
// подробности строки, ход применения, итог.
func (m *Model) lowerPane(width int) (string, string) {
	switch m.stage {
	case stageRunning:
		return "Ход дела", m.progressBody(width)
	case stageDone:
		return "Итог", m.reportBody(width)
	case stageError:
		return "Ошибка", strings.Join(wrap(m.err.Error(), width), "\n")
	case stageConfirm:
		return "Подтверждение", m.confirmBody(width)
	}
	if r := m.current(); r != nil && (r.step != nil || r.note != nil) {
		return "Подробности", m.detailBody(r, width)
	}
	return "Выбрано", m.summaryBody(width)
}

func (m *Model) summaryBody(width int) string {
	p := m.selectedPlan()
	if len(p.Steps) == 0 {
		return m.theme.dim.Render("ничего не отмечено")
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("%s в %s",
		ru.Step(len(p.Steps)), ru.InAreas(len(p.Providers()))))
	lines = append(lines, "")

	for _, id := range p.Providers() {
		title := id
		for _, r := range m.results {
			if r.Provider == id {
				title = r.Title
			}
		}
		lines = append(lines, truncate(m.theme.change.Render("→ ")+title, width))
		for _, s := range p.Steps {
			if s.Provider == id {
				lines = append(lines, truncate("   "+m.theme.dim.Render(s.Summary), width))
			}
		}
	}
	if m.facts != nil && m.facts.RebootRequired {
		lines = append(lines, "", m.theme.warn.Render("⚠ система просит перезагрузку"))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) detailBody(r *row, width int) string {
	if r.note != nil {
		return strings.Join(wrap(r.note.Message, width), "\n")
	}
	s := r.step
	var lines []string
	lines = append(lines, wrap(s.Summary, width)...)
	lines = append(lines, "")

	switch {
	case len(s.Cmd) > 0:
		lines = append(lines, m.theme.dim.Render("команда:"))
		lines = append(lines, wrap(m.app.Masker.Apply(strings.Join(s.Cmd, " ")), width)...)
	case s.Diff != "":
		lines = append(lines, m.theme.dim.Render("правка "+s.Path+":"))
		for _, l := range strings.Split(s.Diff, "\n") {
			lines = append(lines, m.colorDiff(truncate(l, width)))
		}
	case s.Path != "":
		lines = append(lines, m.theme.dim.Render("путь: ")+s.Path)
	}

	for _, g := range s.Guards {
		if g.Why != "" {
			lines = append(lines, "", m.theme.warn.Render("перед этим: "+g.Why))
		}
	}
	for _, u := range s.Unknown {
		lines = append(lines, "", m.theme.dim.Render("? "+u))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) colorDiff(line string) string {
	switch {
	case strings.HasPrefix(line, "+"):
		return m.theme.ok.Render(line)
	case strings.HasPrefix(line, "-"):
		return m.theme.bad.Render(line)
	case strings.HasPrefix(line, "@@"):
		return m.theme.change.Render(line)
	}
	return line
}

func (m *Model) confirmBody(width int) string {
	p := m.selectedPlan()
	lines := []string{
		fmt.Sprintf("Применить %s в %s?", ru.Step(len(p.Steps)), ru.InAreas(len(p.Providers()))),
		"",
	}
	for _, s := range p.Steps {
		lines = append(lines, truncate("• "+s.Summary, width))
	}
	lines = append(lines, "", m.theme.warn.Render("Enter — применить, n — вернуться к выбору"))
	return strings.Join(lines, "\n")
}

func (m *Model) progressBody(width int) string {
	if len(m.progress) == 0 {
		return m.theme.dim.Render("ожидание…")
	}
	var lines []string
	for _, l := range m.progress {
		lines = append(lines, truncate(l, width))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) reportBody(width int) string {
	r := m.report
	var lines []string
	lines = append(lines, m.theme.ok.Render("выполнено: ")+ru.Step(len(r.Done)))

	for _, g := range r.Guarded {
		lines = append(lines, "", m.theme.warn.Render("не начато: "+g.Step.Summary))
		lines = append(lines, wrap(g.Err.Error(), width)...)
	}
	for _, f := range r.Failed {
		lines = append(lines, "", m.theme.bad.Render("не удалось: "+f.Step.Summary))
		lines = append(lines, wrap(f.Err.Error(), width)...)
	}
	for _, s := range r.Skipped {
		lines = append(lines, m.theme.dim.Render("пропущено: "+s.Summary))
	}
	if r.Aborted {
		lines = append(lines, m.theme.warn.Render("прервано по твоему решению"))
	}
	if path := m.app.Log.Path(); path != "" {
		lines = append(lines, "", m.theme.dim.Render("лог: "+path))
	}
	return strings.Join(lines, "\n")
}

// viewHelp — все клавиши на одном экране.
func (m *Model) viewHelp() string {
	rows := [][2]string{
		{"↑ ↓ / k j", "переместиться по списку"},
		{"пробел", "отметить область"},
		{"enter", "развернуть: показать шаги"},
		{"a", "применить отмеченное"},
		{"p", "пересчитать: собрать план заново"},
		{"A / N", "отметить все / снять все"},
		{"tab", "переключить панель"},
		{"?", "эта справка"},
		{"q", "выход"},
	}

	var b strings.Builder
	b.WriteString(m.theme.title.Render("Клавиши") + "\n\n")
	for _, r := range rows {
		b.WriteString("  " + m.theme.change.Render(pad(r[0], 12)) + r[1] + "\n")
	}
	b.WriteString("\n" + m.theme.dim.Render(
		"Работает и на русской раскладке: ф — применить, з — пересчитать, й — выход.") + "\n")
	b.WriteString(m.theme.dim.Render(
		"Значки: → изменится · ✓ уже в порядке · · нет в манифесте · ✗ ошибка") + "\n")

	body := m.height - 2
	return lipgloss.JoinVertical(lipgloss.Left,
		m.theme.pane("Помощь", b.String(), m.width-2, body, true),
		m.theme.footer.Render(" [?] или [q] — закрыть"))
}

func pad(s string, width int) string {
	if n := lipgloss.Width(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}

func shortPVE(v string) string {
	i := strings.Index(v, "pve-manager/")
	if i < 0 {
		return v
	}
	rest := v[i+len("pve-manager/"):]
	if j := strings.IndexByte(rest, '/'); j > 0 {
		return "PVE " + rest[:j]
	}
	return v
}
