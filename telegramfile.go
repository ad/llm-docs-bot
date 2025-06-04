package main

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/go-telegram/bot"
)

// downloadFile скачивает файл по URL и сохраняет во временный файл
func downloadFile(fileURL string) (string, error) {
	resp, err := http.Get(fileURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("не удалось скачать файл: %s", resp.Status)
	}

	tmpFile, err := os.CreateTemp(".", "file-*")
	if err != nil {
		return "", err
	}

	_, err = io.Copy(tmpFile, resp.Body)
	tmpFile.Close()
	if err != nil {
		os.Remove(tmpFile.Name())
		return "", err
	}

	return tmpFile.Name(), nil
}

// downloadTxtFile скачивает и возвращает текст из .txt файла по ссылке
func downloadTxtFile(fileURL string) (string, error) {
	tmpFile, err := downloadFile(fileURL)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpFile)

	body, err := os.ReadFile(tmpFile)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// WordDocument представляет структуру для парсинга document.xml из DOCX
type WordDocument struct {
	Body struct {
		Paragraphs []Paragraph `xml:"p"`
	} `xml:"body"`
}

type Paragraph struct {
	Runs []Run `xml:"r"`
}

type Run struct {
	Text string `xml:"t"`
}

// downloadDocxFile скачивает .docx, извлекает текст и удаляет временный файл
func downloadDocxFile(fileURL string) (string, error) {
	tmpFile, err := downloadFile(fileURL)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpFile)

	return extractTextFromDocx(tmpFile)
}

// extractTextFromDocx извлекает текст из DOCX файла
func extractTextFromDocx(filename string) (string, error) {
	reader, err := zip.OpenReader(filename)
	if err != nil {
		return "", err
	}
	defer reader.Close()

	for _, file := range reader.File {
		if file.Name == "word/document.xml" {
			rc, err := file.Open()
			if err != nil {
				return "", err
			}
			defer rc.Close()

			content, err := io.ReadAll(rc)
			if err != nil {
				return "", err
			}

			var doc WordDocument
			err = xml.Unmarshal(content, &doc)
			if err != nil {
				return "", err
			}

			var sb strings.Builder
			for _, para := range doc.Body.Paragraphs {
				for _, run := range para.Runs {
					sb.WriteString(run.Text)
				}
				sb.WriteString("\n")
			}

			return sb.String(), nil
		}
	}

	return "", fmt.Errorf("не найден document.xml в DOCX файле")
}

// Исправленная функция получения прямой ссылки на файл Telegram
func getTelegramFileURL(ctx context.Context, b *bot.Bot, fileID string) (string, error) {
	file, err := b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return "", err
	}
	apiURL := "https://api.telegram.org/file/bot" + b.Token() + "/" + file.FilePath
	return apiURL, nil
}
