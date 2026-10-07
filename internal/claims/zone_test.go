package claims

import (
	"encoding/base64"
	"testing"
)

func TestNormalizeZoneПриводитКОдномуВиду(t *testing.T) {
	same := []string{
		"internal/keygen",
		"internal/keygen/",
		"./internal/keygen",
		"  internal/keygen  ",
		"internal//keygen",
		"internal/bus/../keygen",
	}

	for _, in := range same {
		got, err := NormalizeZone(in)
		if err != nil {
			t.Errorf("зона %q отвергнута: %v", in, err)
			continue
		}
		// Без приведения к одному виду захват `internal/keygen` не мешал бы
		// взять `internal/keygen/`, и реестр молча перестал бы работать.
		if got != "internal/keygen" {
			t.Errorf("зона %q приведена к %q", in, got)
		}
	}
}

func TestNormalizeZoneОтвергаетОпасное(t *testing.T) {
	bad := map[string]string{
		"":                  "пустая",
		"   ":               "из пробелов",
		".":                 "весь репозиторий",
		"/etc/passwd":       "от корня файловой системы",
		"../../secrets":     "выход за пределы репозитория",
		"internal/../../..": "выход за пределы после сокращения",
	}

	for in, what := range bad {
		if got, err := NormalizeZone(in); err == nil {
			t.Errorf("зона %q (%s) принята как %q", in, what, got)
		}
	}
}

func TestOverlapsЛовитВложенность(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
		why  string
	}{
		{"internal/keygen", "internal/keygen", true, "одна и та же зона"},
		{"internal", "internal/keygen", true, "держащий каталог работает и внутри"},
		{"internal/keygen", "internal", true, "то же самое наоборот"},
		{"internal/keygen", "internal/bus", false, "соседние каталоги независимы"},
		{"README.md", "internal", false, "разные места"},

		// Ловушка, ради которой граница проверяется по разделителю: по префиксу
		// строки эти зоны выглядят вложенными, хотя это разные каталоги.
		// Ложный отказ загнал бы людей мимо реестра, а тогда он не нужен вовсе.
		{"internal/keygen", "internal/keygen-old", false, "имя-приставка не вложенность"},
		{"internal/key", "internal/keygen", false, "обрезанное имя не родитель"},
		{"cmd", "cmdx/main.go", false, "приставка без разделителя"},
	}

	for _, c := range cases {
		if got := Overlaps(c.a, c.b); got != c.want {
			t.Errorf("Overlaps(%q, %q) = %v, ожидалось %v — %s", c.a, c.b, got, c.want, c.why)
		}
	}
}

// Ключ KV не может начинаться с точки, а такие файлы в репозитории есть.
func TestKeyВыдерживаетФайлыСТочки(t *testing.T) {
	for _, zone := range []string{".gitignore", ".env.example", "internal/keygen", "README.md"} {
		key := Key(zone)

		if key[0] == '.' || key[0] == '$' {
			t.Errorf("ключ %q начинается с недопустимого символа", key)
		}
		// Пустой токен между точками делает тему NATS недопустимой.
		for i := 1; i < len(key); i++ {
			if key[i] == '.' && key[i-1] == '.' {
				t.Errorf("ключ %q содержит пустой токен", key)
			}
		}
		if back, err := ZoneFromKey(key); err != nil || back != zone {
			t.Errorf("обратное преобразование %q дало %q", key, back)
		}
	}
}

// Пути, которые и так годятся в ключ, кодировать нельзя: захваты, взятые
// прежней версией, лежат под сырыми ключами, и после обновления их надо
// по-прежнему находить и снимать.
func TestKeyНеМеняетКлючиДопустимыхПутей(t *testing.T) {
	for _, zone := range []string{"internal/keygen", "tn-generator/deploy/fishki-stand", ".env.example", "a=b/c-d_e.go"} {
		if got, want := Key(zone), "z_"+zone; got != want {
			t.Errorf("Key(%q) = %q, прежний ключ %q", zone, got, want)
		}
	}
}

// Остальные пути кодируются обратимо и дают допустимый ключ.
func TestKeyКодируетНедопустимыеПути(t *testing.T) {
	for _, zone := range []string{
		"docs/php-dual-write/задачи.md",
		"docs/отчёт за день.md",
		"docs/a..b",
		"docs/конец.",
		"docs/a:b",
		"e_не_путать",
		"z_как_приставка",
	} {
		key := Key(zone)
		if !rawKeyValid(key) {
			t.Errorf("ключ %q для %q недопустим в KV", key, zone)
		}
		if back, err := ZoneFromKey(key); err != nil || back != zone {
			t.Errorf("обратное преобразование %q дало %q, %v", key, back, err)
		}
	}
}

// Чужой ключ без приставки — ошибка, а не пустая зона.
func TestZoneFromKeyОтвергаетЧужойКлюч(t *testing.T) {
	for _, key := range []string{
		"internal/keygen",
		"e_!!!",
		"",
		// Неканоничные ключи: разбираются, но Key от результата даёт другой
		// ключ. List прочёл бы по нему чужое место и молча потерял запись.
		"e_YQ", // «a» — её ключ z_a
		"e_YR", // тоже «a»: хвостовые биты не нулевые
		"e_",   // пустая зона
		"e_" + base64.RawURLEncoding.EncodeToString([]byte("docs/./задачи.md")), // не нормализована
		"z_a/../b",
		"z_./x",
		"z_a/",
	} {
		if z, err := ZoneFromKey(key); err == nil {
			t.Errorf("ключ %q разобран в %q без ошибки", key, z)
		}
	}
}
