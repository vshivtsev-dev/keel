package cli

import (
	"context"
	"fmt"

	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/netcheck"
	"github.com/vshivtsev-dev/keel/internal/plan"
	"github.com/vshivtsev-dev/keel/internal/provider/host"
)

// Guards — как keel проверяет условия шагов. Открыты наружу: тем же
// набором пользуется экран.
func Guards(f *facts.Facts) map[plan.GuardKind]exec.GuardFunc { return guards(f) }

// guards — как keel проверяет условия шагов. Проверка живёт здесь, а не в
// провайдере: провайдер описывает, что должно быть, и в сеть не ходит.
func guards(f *facts.Facts) map[plan.GuardKind]exec.GuardFunc {
	return map[plan.GuardKind]exec.GuardFunc{
		plan.GuardNetSpeed: func(ctx context.Context, arg string) string {
			return checkNetSpeed(ctx, f, arg)
		},
	}
}

// checkNetSpeed возвращает причину, по которой начинать закачку не стоит.
// Пустая строка — связь достаточная.
func checkNetSpeed(ctx context.Context, f *facts.Facts, min string) string {
	want := host.SpeedToBytes(min)
	if want <= 0 {
		return "" // проверка выключена в манифесте
	}
	if f.UpgradeURI == "" {
		return "" // качать нечего — и пробовать нечего
	}

	got, err := netcheck.Default().Speed(ctx, f.UpgradeURI)
	if err != nil || got == 0 {
		return fmt.Sprintf("репозиторий не отвечает: %s.\n"+
			"    Обновление не начинаю: на неотвечающей сети apt копит очередь закачек в памяти, пока хост не кончится.\n"+
			"    keel doctor покажет, не уходят ли попытки apt в IPv6, до которого нет маршрута — это самая частая причина.",
			f.UpgradeURI)
	}
	if got >= want {
		return ""
	}

	why := fmt.Sprintf("скорость до репозитория %d КБ/с — этого мало (нужно хотя бы %d КБ/с)",
		got/1024, want/1024)
	if f.UpgradeBytes > 0 {
		mins := f.UpgradeBytes / got / 60
		if mins < 1 {
			mins = 1
		}
		why += fmt.Sprintf(".\n    Скачать %d МБ на такой скорости — около %s, и всё это время apt копит очередь закачек в памяти",
			f.UpgradeBytes/(1<<20), plural(int(mins), "минуту", "минуты", "минут"))
	}
	return why + ".\n    Порог задаётся ключом host.updates_min_speed, «0» его выключает"
}
