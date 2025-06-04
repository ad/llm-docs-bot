package main

// Импортируем необходимые библиотеки
import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	_ "github.com/joho/godotenv/autoload"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/ollama"
	"github.com/tmc/langchaingo/prompts"
)

type Session struct {
	Chain        chains.Chain
	Mutex        sync.Mutex
	DocumentText string
}

var (
	sessions = make(map[int64]*Session)
	llm      llms.Model
)

func main() {
	// Проверка наличия токена
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("[ERROR] Не найден TELEGRAM_BOT_TOKEN в .env")
	}

	var err error
	llm, err = ollama.New(
		ollama.WithModel("gemma3:1b"),
	)
	if err != nil {
		log.Fatalf("[ERROR] Ошибка инициализации LLM: %v", err)
	}

	b, err := bot.New(token)
	if err != nil {
		log.Fatalf("[ERROR] Ошибка запуска Telegram-бота: %v", err)
	}

	log.Println("Бот запущен. Ожидание сообщений...")

	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchType(bot.HandlerTypeMessageText), func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message != nil {
			if update.Message.Document != nil {
				handleDocument(ctx, b, update.Message)

				return
			}

			if update.Message.Text == "/start" {
				// Отправляем приветственное сообщение при старте
				b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: update.Message.Chat.ID,
					Text:   "Привет! Я бот для работы с документами. Загрузите .txt или .docx файл, и я помогу вам ответить на вопросы по его содержимому.",
				})
				log.Printf("[INFO] @%s (%d) запустил бота", update.Message.From.Username, update.Message.Chat.ID)
			}
		}
	})

	// Регистрация обработчиков для вопросов
	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypeContains, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message != nil && update.Message.Document == nil {
			handleText(ctx, b, update.Message)
		}
	})

	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "delete", bot.MatchTypeExact, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.CallbackQuery != nil && update.CallbackQuery.Message.Message != nil {
			handleDelete(ctx, b, update.CallbackQuery.Message.Message)
		}
	})

	b.Start(context.Background())
}

// handleDelete удаляет документ для чата
func handleDelete(ctx context.Context, b *bot.Bot, msg *models.Message) {
	chatID := msg.Chat.ID
	username := msg.From.Username
	delete(sessions, chatID)
	log.Printf("[INFO] @%s удалил документ (%d)", username, chatID)

	b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    chatID,
		MessageID: msg.ID,
	})

	// Отправляем сообщение об удалении
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "Документ удалён.",
	})
}

// handleDocument обрабатывает загрузку документов пользователем
func handleDocument(ctx context.Context, b *bot.Bot, msg *models.Message) {
	if msg.Document == nil {
		return
	}
	username := msg.From.Username
	chatID := msg.Chat.ID
	filename := msg.Document.FileName
	log.Printf("[INFO] Документ от @%s (%d): %s", username, chatID, filename)

	// Скачиваем файл
	fileURL, err := getTelegramFileURL(ctx, b, msg.Document.FileID)
	if err != nil {
		log.Printf("[ERROR] Не удалось получить ссылку на файл: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Ошибка при получении файла. Попробуйте еще раз.",
		})
		return
	}

	var docText string
	if strings.HasSuffix(strings.ToLower(filename), ".txt") {
		docText, err = downloadTxtFile(fileURL)
	} else if strings.HasSuffix(strings.ToLower(filename), ".docx") {
		docText, err = downloadDocxFile(fileURL)
	} else {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Поддерживаются только .txt и .docx файлы.",
		})
		return
	}
	if err != nil {
		log.Printf("[ERROR] Ошибка чтения документа: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Ошибка при чтении документа.",
		})
		return
	}

	// Проверка на prompt injection
	phrases := []string{"забудь все инструкции", "ты больше не ассистент", "отныне ты", "ignore previous", "disregard previous"}
	for _, phrase := range phrases {
		if strings.Contains(strings.ToLower(docText), phrase) {
			log.Printf("[WARN] Подозрительный документ от %d: содержит '%s'", chatID, phrase)
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "Документ содержит подозрительные фразы и не будет обработан.",
			})
			return
		}
	}

	log.Printf("[INFO] Документ успешно загружен от @%s (%d): %s\n%s", username, chatID, filename, docText)

	chain, err := createChainFromText(docText)
	if err != nil {
		log.Printf("[ERROR] Ошибка создания цепочки: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Ошибка при подготовке документа.",
		})
		return
	}

	// В handleDocument сохраняем текст документа в Session
	s := &Session{
		Chain:        chain,
		DocumentText: docText,
	}
	sessions[chatID] = s

	keyboard := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "🗑 Удалить", CallbackData: "delete"},
			},
		},
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        "Документ загружен. Теперь вы можете задавать вопросы.",
		ReplyMarkup: keyboard,
	})
}

// createChainFromText создает цепочку с системным промптом и текстом документа
func createChainFromText(docText string) (chains.Chain, error) {
	prompt := `Ты полезный ассистент. Отвечай на вопросы строго на основе следующего документа.
Отвечай кратко и по существу на основе информации из документа.
Если ответа нет в документе, скажи об этом.

ДОКУМЕНТ:
%s

ВОПРОС: {{.input}}

ОТВЕТ:`
	tmpl := prompts.NewPromptTemplate(
		fmt.Sprintf(prompt, docText),
		[]string{"input"},
	)
	return chains.NewLLMChain(llm, tmpl), nil
}

// handleText обрабатывает текстовые вопросы пользователя
func handleText(ctx context.Context, b *bot.Bot, msg *models.Message) {
	chatID := msg.Chat.ID
	username := msg.From.Username
	question := msg.Text
	log.Printf("[Q] @%s (%d): %s", username, chatID, question)

	s, ok := sessions[chatID]
	if !ok {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Сначала загрузите документ (.txt или .docx).",
		})
		return
	}

	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	resp, err := s.Chain.Call(ctx, map[string]any{"input": question})
	if err != nil {
		log.Printf("[ERROR] Ошибка генерации ответа: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Ошибка генерации ответа от LLM.",
		})
		return
	}
	answer, _ := resp["text"].(string)
	log.Printf("[A] -> %s", answer)
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   answer,
	})
}
