package ui

import (
	"os"
	"testing"
)

// TestDump печатает экран целиком — чтобы на него можно было посмотреть
// глазами, а не только сверять подстроки. Запуск:
//
//	go test ./internal/ui -run TestDump -v
func TestDump(t *testing.T) {
	if os.Getenv("KEEL_DUMP") == "" {
		t.Skip("включается через KEEL_DUMP=1")
	}
	for _, size := range [][2]int{{120, 30}, {45, 22}} {
		m := testModel(t, size[0], size[1])
		t.Logf("\n=== %d×%d ===\n%s", size[0], size[1], m.View())
	}
}
