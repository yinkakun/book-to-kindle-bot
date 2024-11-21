# Book to Kindle Telegram Bot

Send eBooks from Telegram directly to your Kindle via your kindle email. Supports PDF, EPUB, and MOBI files.

## How It Works

1. Set your Kindle email address with the bot
2. Add the bot's email address to your Kindle's approved email list
3. Send or forward ebook file to the bot
4. The bot automatically emails the book to your Kindle

## Bot Commands

- `/start`: Get started and see basic instructions
- `/help`: View all available commands
- `/set_kindle_email`: Configure your Kindle email address

## Notes

- Only Kindle email addresses (ending with @kindle.com) are accepted
- Maximum file size is strictly enforced at 20MB

## Local Development

### Accounts & Credentials

- Telegram account and bot token from BotFather
- SMTP credentials from AWS SES

1. Clone the repository

   ```bash
   git clone https://github.com/yinkakun/book-to-kindle-bot.git
   cd book-to-kindle-bot
   ```

2. Install dependencies

   ```bash
   go mod tidy
   ```

3. Create a `.env` file with required variables

   ```env
   DB_PATH=./db.sqlite
   BOT_EMAIL=your-bot-email@example.com # Email used to send books
   TELEGRAM_BOT_TOKEN=your_telegram_bot_token
   AWS_SES_SMTP_USERNAME=your_ses_username
   AWS_SES_SMTP_PASSWORD=your_ses_password
   ```

4. Run the application

   ```bash
    go run main.go
   ```

## Disclaimer

This project is not affiliated with Amazon, Kindle, or Telegram. Use responsibly and respect copyright laws.
