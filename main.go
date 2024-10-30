package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsConfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"
)

type BotConfig struct {
	DbPath        string
	BotEmail      string
	TelegramToken string

	MaxFileSize     int
	DownloadTimeout time.Duration
}

const dbSchema = `
	CREATE TABLE IF NOT EXISTS users (
		kindle_email TEXT NOT NULL,
		telegram_id INTEGER PRIMARY KEY,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS sent_books (
		book_name TEXT NOT NULL,
		file_size INTEGER NOT NULL,
		telegram_id INTEGER NOT NULL,
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(telegram_id) REFERENCES users(telegram_id)
	);
`

type Db struct {
	*sql.DB
}

type BookToKindleBot struct {
	db             *Db
	config         BotConfig
	sesClient      *ses.Client
	httpClient     *http.Client
	telegramBotApi *tgbotapi.BotAPI
}

var supportedMimeTypes = map[string]bool{
	"application/pdf":                true,
	"application/epub+zip":           true,
	"application/vnd.amazon.ebook":   true,
	"application/x-mobipocket-ebook": true,
}

/*
 * Database methods
 */

func NewDb(dbPath string) (*Db, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("error opening SQLite database: %w", err)
	}

	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)

	if _, err := db.Exec(dbSchema); err != nil {
		return nil, fmt.Errorf("error creating database schema: %w", err)
	}

	return &Db{db}, nil
}

func (db *Db) GetKindleEmail(ctx context.Context, telegramId int64) (string, error) {
	var kindleEmail string
	err := db.QueryRowContext(ctx, "SELECT kindle_email FROM users WHERE telegram_id = ?", telegramId).Scan(&kindleEmail)
	return kindleEmail, err
}

func (db *Db) SetKindleEmail(ctx context.Context, telegramId int64, kindleEmail string) error {
	_, err := db.ExecContext(ctx, `
        INSERT INTO users (telegram_id, kindle_email) VALUES (?, ?)
        ON CONFLICT(telegram_id) DO UPDATE SET kindle_email = ?
    `, telegramId, kindleEmail, kindleEmail)
	return err
}

func (db *Db) logSentBook(ctx context.Context, telegramId int64, bookName string, fileSize int) error {
	_, err := db.ExecContext(ctx, "INSERT INTO sent_books (book_name, file_size, telegram_id) VALUES (?, ?, ?)", bookName, fileSize, telegramId)
	return err
}

/*
 * Bot methods
 */

func NewBookToKindleBot(config BotConfig) (*BookToKindleBot, error) {
	telegramBotApi, err := tgbotapi.NewBotAPI(config.TelegramToken)
	if err != nil {
		return nil, fmt.Errorf("error creating telegram bot API: %w", err)
	}

	telegramBotApi.Debug = true

	awsConfig, err := awsConfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("error loading AWS config: %w", err)
	}

	db, err := NewDb(config.DbPath)
	if err != nil {
		return nil, fmt.Errorf("error creating database: %w", err)
	}

	return &BookToKindleBot{
		db:             db,
		config:         config,
		telegramBotApi: telegramBotApi,
		sesClient:      ses.NewFromConfig(awsConfig),
		httpClient:     &http.Client{Timeout: config.DownloadTimeout},
	}, nil
}

func (b *BookToKindleBot) Run(ctx context.Context) error {
	const directoryPermission = 0755 // rwxr-xr-x
	if err := os.Mkdir("downloads", directoryPermission); err != nil {
		return fmt.Errorf("error creating downloads directory: %w", err)
	}

	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = int(time.Second * 60)
	updates := b.telegramBotApi.GetUpdatesChan(updateConfig)

	for {
		select {
		case update := <-updates:
			if update.Message != nil {
				go b.handleUpdate(ctx, update)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (b *BookToKindleBot) handleUpdate(ctx context.Context, update tgbotapi.Update) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("recovered from panic in handleUpdate",
				"error", r,
				"user_id", update.Message.From.ID,
				"chat_id", update.Message.Chat.ID,
			)
		}
	}()

	if update.Message.Document != nil {
		b.handleDocument(ctx, update)
		return
	}

	if update.Message.IsCommand() {
		b.handleCommand(ctx, update)
		return
	}
}

func (b *BookToKindleBot) handleDocument(ctx context.Context, update tgbotapi.Update) {
	kindleEmail, err := b.db.GetKindleEmail(ctx, update.Message.From.ID)
	if err != nil {
		b.sendMessage(update.Message.Chat.ID, "Please set your Kindle email address first using /set_kindle_email")
		return
	}

	if !supportedMimeTypes[update.Message.Document.MimeType] {
		b.sendMessage(update.Message.Chat.ID, "Unsupported file type. Try sending a PDF, EPUB, or MOBI file")
		return
	}

	if update.Message.Document.FileSize > b.config.MaxFileSize {
		b.sendMessage(update.Message.Chat.ID, "File is too large. Maximum file size is 20MB")
		return
	}

	b.sendMessage(update.Message.Chat.ID, "Sending book to Kindle...")

	fileBytes, err := b.downloadTelegramFile(update.Message.Document.FileID)
	if err != nil {
		slog.Error("error downloading file", "error", err, "user_id", update.Message.From.ID, "file_id", update.Message.Document.FileID)
		b.sendMessage(update.Message.Chat.ID, "Error downloading file, please try again later")
		return
	}

	_, err = b.sesClient.SendRawEmail(ctx, &ses.SendRawEmailInput{
		Source:       aws.String(b.config.BotEmail),
		RawMessage:   &types.RawMessage{Data: fileBytes},
		Destinations: []string{kindleEmail},
	})
	if err != nil {
		slog.Error("error sending email", "error", err, "user_id", update.Message.From.ID, "kindle_email", kindleEmail)
		b.sendMessage(update.Message.Chat.ID, "Error sending email, please try again later")
		return
	}

	if err := b.db.logSentBook(ctx, update.Message.From.ID, update.Message.Document.FileName, update.Message.Document.FileSize); err != nil {
		slog.Error("error logging sent book", "error", err, "user_id", update.Message.From.ID, "file_name", update.Message.Document.FileName)
	}

	b.sendMessage(update.Message.Chat.ID, "Book sent to Kindle successfully")
}

func (b *BookToKindleBot) sendMessage(chatId int64, text string) {
	msg := tgbotapi.NewMessage(chatId, text)
	if _, err := b.telegramBotApi.Send(msg); err != nil {
		slog.Error("failed to send message", "error", err, "chat_id", chatId)
	}
}

/*
 * Command handlers
 */

func (b *BookToKindleBot) handleCommand(ctx context.Context, update tgbotapi.Update) {
	switch update.Message.Command() {
	case "start":
		b.startCommand(update)
	case "help":
		b.helpCommand(update)
	case "set_kindle_email":
		b.setKindleEmailCommand(ctx, update)
	default:
		b.invalidCommand(update)
	}
}

func (b *BookToKindleBot) invalidCommand(update tgbotapi.Update) {
	message := fmt.Sprintf("Unknown command: %s, use /help for available commands", update.Message.Command())
	b.sendMessage(update.Message.Chat.ID, message)
}

func (b *BookToKindleBot) startCommand(update tgbotapi.Update) {
	message := fmt.Sprintf(`
		Hello %s! Send me a PDF, EPUB, or MOBI file and I'll send it to your Kindle.
		Use /set_kindle_email to set your Kindle email address and don't forget to whitelist %s in your Kindle settings.
	`, update.Message.From.FirstName, b.config.BotEmail)

	b.telegramBotApi.Send(tgbotapi.NewMessage(update.Message.Chat.ID, message))
}

func (b *BookToKindleBot) helpCommand(update tgbotapi.Update) {
	message := `
		Available commands:
		/set_kindle_email <kindle_email_address> - set your Kindle email address
		/help - show this help message
	`
	b.telegramBotApi.Send(tgbotapi.NewMessage(update.Message.Chat.ID, message))
}

func (b *BookToKindleBot) setKindleEmailCommand(ctx context.Context, update tgbotapi.Update) {
	args := update.Message.CommandArguments()
	if args == "" {
		b.sendMessage(update.Message.Chat.ID, "Please provide your Kindle email address")
		return
	}

	kindleEmail, err := validateEmail(args)
	if err != nil {
		b.sendMessage(update.Message.Chat.ID, err.Error())
		return
	}

	if err := b.db.SetKindleEmail(ctx, update.Message.From.ID, kindleEmail); err != nil {
		b.sendMessage(update.Message.Chat.ID, "Error setting Kindle email address, please try again later")
		slog.Error("error setting kindle email", "error", err, "user_id", update.Message.From.ID, "kindle_email", kindleEmail)
		return
	}

	b.sendMessage(update.Message.Chat.ID, fmt.Sprintf("Kindle email address set to %s successfully", kindleEmail))
}

/*
 * Helper functions
 */

func validateEmail(email string) (string, error) {
	address, err := mail.ParseAddress(email)

	if err != nil {
		return "", fmt.Errorf("invalid email address: %w", err)
	}

	if !strings.HasSuffix(address.Address, "@kindle.com") {
		return "", fmt.Errorf("email address is not a kindle email address")
	}

	return email, nil
}

func (b *BookToKindleBot) downloadTelegramFile(fileId string) ([]byte, error) {
	file, err := b.telegramBotApi.GetFile(tgbotapi.FileConfig{FileID: fileId})
	if err != nil {
		return nil, fmt.Errorf("error getting file info: %w", err)
	}

	fileUrl, err := b.telegramBotApi.GetFileDirectURL(file.FilePath)
	if err != nil {
		return nil, fmt.Errorf("error getting file URL: %w", err)
	}

	resp, err := b.httpClient.Get(fileUrl)
	if err != nil {
		return nil, fmt.Errorf("error downloading file: %w", err)
	}

	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, int64(b.config.MaxFileSize)))
}

/*
 * Main function
 */

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file, ", err)
	}

	dbPath := os.Getenv("DB_PATH")
	botEmail := os.Getenv("BOT_EMAIL")
	telegramToken := os.Getenv("TELEGRAM_BOT_TOKEN")

	if dbPath == "" {
		log.Fatal("DB_PATH environment variable is required")
	}

	if botEmail == "" {
		log.Fatal("BOT_EMAIL environment variable is required")
	}

	if telegramToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	bookToKindleBot, err := NewBookToKindleBot(BotConfig{
		DbPath:          dbPath,
		BotEmail:        botEmail,
		TelegramToken:   telegramToken,
		MaxFileSize:     20 * 1024 * 1024,
		DownloadTimeout: 30 * time.Second,
	})

	if err != nil {
		slog.Error("error creating bot", "error", err)
	}

	slog.Info("starting bot", "username", bookToKindleBot.telegramBotApi.Self.UserName, "bot_email", botEmail)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := bookToKindleBot.Run(ctx); err != nil {
		slog.Error("error running bot", "error", err)
	}
}