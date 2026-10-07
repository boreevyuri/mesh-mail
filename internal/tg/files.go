package tg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// MaxFileBytes — предел размера файла, который мост берёт из Telegram.
//
// 20 МБ — потолок самого Bot API: getFile больший файл не отдаёт. Предел
// нужен не ради этого потолка, а на случай, когда сервер о размере солжёт:
// файл целиком лежит в памяти моста, а мост несёт обратный канал человека.
const MaxFileBytes = 20 << 20

// FetchFile скачивает файл по его file_id: getFile за путём, затем скачивание.
//
// Токен нужен ОБОИМ вызовам, и он есть только здесь, у моста. Ровно поэтому
// байты качает мост, а не адресат: агенту токен не выдаётся. Скачанное мост
// кладёт в ObjectStore (bus.PutAttachment), в письмо идёт лишь ключ.
//
// Идёт своим HTTP, а не через библиотеку: getFile — чтение, ему не нужны ни
// ограничитель частоты, ни повтор на 429, которые библиотечный путь несёт для
// ИСХОДЯЩИХ постов; а скачивание файла и вовсе не метод Bot API, это отдельный
// файловый URL.
func (c *Client) FetchFile(ctx context.Context, fileID string) ([]byte, error) {
	if c.initErr != nil {
		return nil, c.initErr
	}

	path, err := c.filePath(ctx, fileID)
	if err != nil {
		return nil, err
	}
	return c.download(ctx, path)
}

// filePath просит у Telegram путь файла по его file_id.
func (c *Client) filePath(ctx context.Context, fileID string) (string, error) {
	u := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", c.baseURL, c.token, url.QueryEscape(fileID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("запрос getFile: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("getFile: %w", withoutURL(err))
	}
	defer func() { _ = resp.Body.Close() }()

	var env struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return "", fmt.Errorf("разбор ответа getFile: %w", err)
	}
	if !env.OK || env.Result.FilePath == "" {
		return "", fmt.Errorf("getFile не дал пути к файлу: %s", env.Description)
	}
	return env.Result.FilePath, nil
}

// download качает файл по пути, выданному getFile.
//
// Путь в URL НЕ экранируется целиком: в нём значимые слэши каталогов
// (`documents/file_123.zip`), и `url.QueryEscape` превратил бы их в %2F.
func (c *Client) download(ctx context.Context, filePath string) ([]byte, error) {
	u := fmt.Sprintf("%s/file/bot%s/%s", c.baseURL, c.token, filePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("запрос файла: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("скачивание файла: %w", withoutURL(err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("скачивание файла: код %d", resp.StatusCode)
	}
	// Читаем на байт больше предела: так «ровно предел» отличим от «больше».
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("чтение тела файла: %w", withoutURL(err))
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("файл больше %d байт: мост такие не берёт", MaxFileBytes)
	}
	return data, nil
}

// withoutURL снимает с ошибки адрес запроса.
//
// Токен стоит в URL обоих запросов, а http.Client.Do на сетевой ошибке
// возвращает *url.Error, чей текст начинается с адреса целиком. Мост печатает
// ошибку в журнал, журнал живёт в systemd — отсюда утечка токена. Метод и
// причина сохраняются, адрес нет.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}
