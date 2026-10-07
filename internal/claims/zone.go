// Package claims — реестр занятых зон кода.
//
// Лекарство от того, что случилось трижды за один день: двое переписывали
// одно и то же место, узнавая об этом уже после пуша. Агент столбит за собой
// путь, остальные это видят.
//
// Блокировка МЯГКАЯ, и это не недоделка. Файлы правятся напрямую, а не через
// mesh, поэтому запретить редактирование технически нечем. Реестр даёт ровно
// две вещи: захват видно всем, и повторный захват занятого отвергается.
// Обещать больше значило бы врать, а ложное спокойствие хуже его отсутствия.
package claims

import (
	"encoding/base64"
	"fmt"
	"path"
	"strings"
)

// NormalizeZone приводит путь к виду, в котором зоны можно сравнивать.
//
// Без этого `internal/keygen`, `internal/keygen/` и `./internal/keygen` были
// бы тремя разными зонами, и захват одной не мешал бы взять две другие —
// реестр молча перестал бы работать ровно там, где нужен.
func NormalizeZone(zone string) (string, error) {
	z := strings.TrimSpace(zone)
	if z == "" {
		return "", fmt.Errorf("пустая зона")
	}
	if strings.HasPrefix(z, "/") {
		return "", fmt.Errorf("зона %q задана от корня файловой системы, нужен путь внутри репозитория", zone)
	}

	z = path.Clean(z)
	if z == "." {
		return "", fmt.Errorf("зона %q — это весь репозиторий; столбите конкретное место", zone)
	}
	// path.Clean оставляет ведущие "..", если выйти выше корня.
	if z == ".." || strings.HasPrefix(z, "../") {
		return "", fmt.Errorf("зона %q выходит за пределы репозитория", zone)
	}

	return z, nil
}

// Overlaps говорит, мешают ли друг другу две зоны.
//
// Мешают, если совпадают или одна вложена в другую: держащий `internal/`
// работает и в `internal/keygen`, поэтому второй захват — то же столкновение,
// хотя ключи разные.
//
// Граница проверяется по разделителю, а не по префиксу строки. Иначе
// `internal/keygen` и `internal/keygen-old` выглядели бы вложенными —
// это разные зоны, и ложный отказ загонял бы людей мимо реестра, после чего
// им перестали бы пользоваться вовсе.
func Overlaps(a, b string) bool {
	return Covers(a, b) || Covers(b, a)
}

// Covers говорит, включает ли зона a зону b целиком.
//
// Направление важно там, где перекрытие своё же: держащий `internal` уже
// работает в `internal/keygen`, и второй захват ему не нужен, а вот держащий
// `internal/keygen` каталог целиком не покрывает — это расширение зоны,
// и молча выдавать его за идемпотентность нельзя.
func Covers(a, b string) bool {
	return a == b || strings.HasPrefix(b, a+"/")
}

// keyPrefix — приставка к ключу KV.
//
// Нужна из-за файлов вроде `.gitignore` и `.env.example`: ключ KV не может
// начинаться с точки, а в теме NATS точка разделяет токены, и `.env.example`
// дал бы пустой первый токен. С приставкой любой путь репозитория становится
// допустимым ключом, оставаясь при этом читаемым глазами в `nats kv ls`.
const keyPrefix = "z_"

// encodedPrefix — приставка ключа, в котором зона закодирована целиком.
//
// Ключ KV допускает лишь `[-/_=.a-zA-Z0-9]`, не терпит `..` и точки в конце.
// А путь репозитория может быть любым: `docs/задачи.md`, `отчёт за день.md`,
// `a..b`. Раньше такой путь шёл в ключ как есть, и захват падал с
// «nats: invalid key». Здесь он кодируется обратимо (base64url без
// дополнения: весь её алфавит допустим в ключе).
//
// Приставки разные, поэтому ключи двух видов не пересекаются: сырой ключ
// всегда начинается с `z_`, закодированный — с `e_`. Пути, которые и так
// годятся в ключ, кодировать не надо: их ключи остаются прежними, и захваты,
// взятые старой версией, по-прежнему видны и снимаются.
const encodedPrefix = "e_"

// keyChars — алфавит ключа KV (nats.go jetstream, validKeyRe).
func keyChars(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '/', c == '_', c == '=', c == '.':
		default:
			return false
		}
	}
	return true
}

// rawKeyValid повторяет проверку ключа из nats.go (keyValid): непустой,
// без точки в начале и в конце, без `..`, только допустимые символы.
func rawKeyValid(key string) bool {
	if key == "" || key[0] == '.' || key[len(key)-1] == '.' || strings.Contains(key, "..") {
		return false
	}
	return keyChars(key)
}

// Key превращает нормализованную зону в ключ KV.
func Key(zone string) string {
	if raw := keyPrefix + zone; rawKeyValid(raw) {
		return raw
	}
	return encodedPrefix + base64.RawURLEncoding.EncodeToString([]byte(zone))
}

// ZoneFromKey — обратное преобразование.
//
// Ключ, который не удаётся разобрать, — не наш: его положили в бакет мимо
// Key. Ошибка, а не пустая строка, чтобы такой ключ не превратился молча
// в «зону без имени».
//
// Разобрать мало — ключ должен быть каноническим: зона нормализована, и
// Key от неё даёт ровно этот ключ. Иначе `e_YQ` разобрался бы в «a», List
// прочёл бы захват по `z_a`, не нашёл его и молча выбросил запись.
func ZoneFromKey(key string) (string, error) {
	var zone string
	switch {
	case strings.HasPrefix(key, encodedPrefix):
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, encodedPrefix))
		if err != nil {
			return "", fmt.Errorf("ключ %q: не разобрать закодированную зону: %w", key, err)
		}
		zone = string(b)
	case strings.HasPrefix(key, keyPrefix):
		zone = strings.TrimPrefix(key, keyPrefix)
	default:
		return "", fmt.Errorf("ключ %q без известной приставки", key)
	}

	if n, err := NormalizeZone(zone); err != nil || n != zone || Key(zone) != key {
		return "", fmt.Errorf("ключ %q неканонический: положен в реестр мимо Key", key)
	}
	return zone, nil
}
