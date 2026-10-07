package tg_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boreevyuri/mesh-mail/internal/tg"
)

// FetchFile проходит два шага: getFile за путём, затем скачивание по нему.
// Токен есть только здесь, у моста; отсюда и качаем.
func TestFetchFileКачаетДваШага(t *testing.T) {
	const body = "содержимое присланного файла"
	var gotGetFile, gotDownload bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/getFile"):
			gotGetFile = true
			_, _ = io.WriteString(w, `{"ok":true,"result":{"file_path":"documents/example.zip"}}`)
		case strings.Contains(r.URL.Path, "/file/bot"):
			gotDownload = true
			// Путь каталога должен сохраниться, а не превратиться в %2F.
			if !strings.HasSuffix(r.URL.Path, "/documents/example.zip") {
				t.Errorf("путь файла искажён: %s", r.URL.Path)
			}
			_, _ = io.WriteString(w, body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := tg.New("тест-токен", tg.WithBaseURL(srv.URL), tg.WithHTTPClient(srv.Client()))
	data, err := c.FetchFile(context.Background(), "BQACfileID")
	if err != nil {
		t.Fatalf("FetchFile: %v", err)
	}
	if string(data) != body {
		t.Fatalf("содержимое = %q, ожидалось %q", data, body)
	}
	if !gotGetFile || !gotDownload {
		t.Fatalf("не оба шага сделаны: getFile=%v download=%v", gotGetFile, gotDownload)
	}
}

// Отказ getFile (истёкший file_id) доходит ошибкой, а не пустыми байтами.
func TestFetchFileОшибкаGetFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":false,"description":"file not found"}`)
	}))
	defer srv.Close()

	c := tg.New("тест-токен", tg.WithBaseURL(srv.URL), tg.WithHTTPClient(srv.Client()))
	if _, err := c.FetchFile(context.Background(), "старый"); err == nil {
		t.Fatal("ожидалась ошибка на отказ getFile")
	}
}

// Сетевой отказ не уносит токен в текст ошибки.
//
// Токен стоит в URL обоих запросов, а http.Client.Do на сетевой ошибке
// возвращает *url.Error, в тексте которого URL целиком. Мост печатает эту
// ошибку в журнал, а журнал живёт в systemd дольше любого разговора.
// Проверяются оба шага: getFile и само скачивание.
//
// Токен — настоящей формы, из ASCII. Первая редакция теста брала кириллицу, и
// тест был зелёным на непочиненном коде: в URL кириллица кодируется в
// %D0%A1…, и strings.Contains не находил токен, хотя тот утёк целиком.
func TestFetchFileОшибкаСетиНеНесётТокена(t *testing.T) {
	const токен = "123456:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"

	t.Run("getFile", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		адрес := srv.URL
		srv.Close() // порт закрыт: Do упадёт сетевой ошибкой

		c := tg.New(токен, tg.WithBaseURL(адрес))
		_, err := c.FetchFile(context.Background(), "BQACfileID")
		if err == nil {
			t.Fatal("ожидалась сетевая ошибка")
		}
		if strings.Contains(err.Error(), токен) {
			t.Fatalf("токен в тексте ошибки: %v", err)
		}
	})

	t.Run("скачивание", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/getFile") {
				_, _ = io.WriteString(w, `{"ok":true,"result":{"file_path":"documents/x.zip"}}`)
				return
			}
			// Обрываем соединение посреди ответа — сетевая ошибка на втором шаге.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("сервер не умеет hijack")
			}
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}))
		defer srv.Close()

		c := tg.New(токен, tg.WithBaseURL(srv.URL), tg.WithHTTPClient(srv.Client()))
		_, err := c.FetchFile(context.Background(), "BQACfileID")
		if err == nil {
			t.Fatal("ожидалась сетевая ошибка")
		}
		if strings.Contains(err.Error(), токен) {
			t.Fatalf("токен в тексте ошибки: %v", err)
		}
	})
}

// Файл сверх предела отвергается, а не читается в память целиком.
//
// Мост держит обратный канал человека; съеденная память роняет его вместе с
// этим каналом. Предел действует и тогда, когда сервер о размере солгал, —
// поэтому проверяется чтением, а не заявленным file_size.
func TestFetchFileОтвергаетФайлСверхПредела(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/getFile") {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"file_path":"documents/big.bin"}}`)
			return
		}
		chunk := strings.Repeat("x", 64*1024)
		for written := int64(0); written <= tg.MaxFileBytes; written += int64(len(chunk)) {
			if _, err := io.WriteString(w, chunk); err != nil {
				return // клиент бросил чтение — так и задумано
			}
		}
	}))
	defer srv.Close()

	c := tg.New("тест-токен", tg.WithBaseURL(srv.URL), tg.WithHTTPClient(srv.Client()))
	data, err := c.FetchFile(context.Background(), "BQACfileID")
	if err == nil {
		t.Fatalf("файл сверх предела принят: %d байт", len(data))
	}
}
