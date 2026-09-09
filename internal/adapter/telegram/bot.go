package telegram

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"
	"gopkg.in/telebot.v3/middleware"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

const callbackEndpoint = "rb"

type Response = domain.BotResponse
type Button = domain.BotButton
type Handler = domain.BotHandler

type Config struct {
	Token         string
	AllowedChatID int64
	PollTimeout   time.Duration
	APIRoot       string
	ProxyURL      string
}

type Bot struct {
	client  *tele.Bot
	handler Handler
	logger  *slog.Logger
}

func New(config Config, handler Handler, logger *slog.Logger) (*Bot, error) {
	if strings.TrimSpace(config.Token) == "" {
		return nil, fmt.Errorf("telegram bot token is required")
	}
	if config.AllowedChatID == 0 {
		return nil, fmt.Errorf("allowed Telegram chat ID is required")
	}
	if config.PollTimeout <= 0 {
		config.PollTimeout = 10 * time.Second
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	logger = logger.With("component", "telegram")
	client, err := tele.NewBot(tele.Settings{
		Token: config.Token,
		URL:   config.APIRoot,
		Poller: &tele.LongPoller{
			Timeout:        config.PollTimeout,
			AllowedUpdates: []string{"message", "callback_query"},
		},
		OnError: func(err error, _ tele.Context) {
			logger.Error("telegram handler error", "err", err)
		},
		Client: buildHTTPClient(config.ProxyURL),
	})
	if err != nil {
		return nil, fmt.Errorf("create Telegram bot: %w", err)
	}
	bot := &Bot{client: client, handler: handler, logger: logger}
	bot.register(config.AllowedChatID)
	if err := bot.registerCommands(); err != nil {
		logger.Warn("failed to set Telegram command list", "err", err)
	}
	return bot, nil
}

func (b *Bot) Start() { b.client.Start() }

func (b *Bot) Stop() { b.client.Stop() }

// NotifyTyping posts a "typing…" chat action to the given chat. Telegram
// actions expire on the client after about five seconds, so callers should
// invoke this periodically while a long-running operation is in flight.
func (b *Bot) NotifyTyping(ctx context.Context, chatID int64) error {
	if chatID == 0 {
		return fmt.Errorf("chat id must not be zero")
	}
	if err := b.client.Notify(tele.ChatID(chatID), tele.Typing); err != nil {
		b.logger.Warn("telegram typing notification failed", "err", err, "chat_id", chatID)
		return err
	}
	return nil
}

// SendMessage posts a Markdown-formatted message to the chat. It is used by
// the control surface to push asynchronous notifications (e.g. "task done,
// come back") without going through the regular command/response pipeline.
func (b *Bot) SendMessage(ctx context.Context, chatID int64, text string) error {
	if chatID == 0 {
		return fmt.Errorf("chat id must not be zero")
	}
	html := markdownToTelegramHTML(text)
	if _, err := b.client.Send(tele.ChatID(chatID), html, tele.ModeHTML); err != nil {
		b.logger.Warn("telegram send failed", "err", err, "chat_id", chatID)
		return err
	}
	return nil
}

// SendResponse is the button-enabled variant used for asynchronous
// notifications. It mirrors the markup the regular send() applies for
// command responses so the look-and-feel is consistent.
func (b *Bot) SendResponse(ctx context.Context, chatID int64, response domain.BotResponse) error {
	if chatID == 0 {
		return fmt.Errorf("chat id must not be zero")
	}
	html := markdownToTelegramHTML(response.Text)
	markup := keyboard(response.Buttons)
	options := []interface{}{tele.ModeHTML}
	if markup != nil {
		options = append(options, markup)
	}
	if _, err := b.client.Send(tele.ChatID(chatID), html, options...); err != nil {
		b.logger.Warn("telegram send failed", "err", err, "chat_id", chatID)
		return err
	}
	return nil
}

func (b *Bot) register(allowedChatID int64) {
	authorized := middleware.Whitelist(allowedChatID)
	commands := []string{"/start", "/help", "/status", "/projects", "/init", "/sessions", "/diff", "/changes", "/undo", "/watch", "/continue"}
	for _, command := range commands {
		command := command
		b.client.Handle(command, func(c tele.Context) error {
			response, err := b.handler.HandleCommand(context.Background(), c.Chat().ID, command, c.Args())
			if err != nil {
				return err
			}
			return b.send(c, response)
		}, authorized)
	}
	b.client.Handle(tele.OnText, func(c tele.Context) error {
		response, err := b.handler.HandleText(context.Background(), c.Chat().ID, c.Text())
		if err != nil {
			return err
		}
		return b.send(c, response)
	}, authorized)

	button := &tele.InlineButton{Unique: callbackEndpoint}
	b.client.Handle(button, func(c tele.Context) error {
		if err := c.Respond(); err != nil {
			return err
		}
		response, err := b.handler.HandleCallback(context.Background(), c.Chat().ID, c.Data())
		if err != nil {
			return err
		}
		response.Edit = true
		return b.send(c, response)
	}, authorized)
}

func (b *Bot) registerCommands() error {
	commands := []tele.Command{
		{Text: "start", Description: "👋 Bienvenida y ayuda."},
		{Text: "help", Description: "❓ Lista de comandos."},
		{Text: "projects", Description: "📂 Elegir proyecto."},
		{Text: "init", Description: "🚀 Arrancar servidor."},
		{Text: "status", Description: "💡 Estado actual."},
		{Text: "sessions", Description: "💬 Sesiones activas."},
		{Text: "diff", Description: "📝 Cambios de la sesión."},
		{Text: "changes", Description: "📝 Alias de /diff."},
		{Text: "undo", Description: "↩️ Revertir último cambio."},
		{Text: "watch", Description: "👀 Vigilar una sesión hasta que termine."},
		{Text: "continue", Description: "▶️ Reactivar la última sesión completada."},
	}
	if err := b.client.SetCommands(commands); err != nil {
		return fmt.Errorf("set bot commands: %w", err)
	}
	return nil
}

func (b *Bot) send(c tele.Context, response Response) error {
	markup := keyboard(response.Buttons)
	html := markdownToTelegramHTML(response.Text)
	options := []interface{}{tele.ModeHTML}
	if markup != nil {
		options = append(options, markup)
	}
	if response.Edit {
		return c.Edit(html, options...)
	}
	return c.Send(html, options...)
}

func keyboard(buttons [][]Button) *tele.ReplyMarkup {
	if len(buttons) == 0 {
		return nil
	}
	markup := &tele.ReplyMarkup{}
	rows := make([]tele.Row, 0, len(buttons))
	for _, buttonRow := range buttons {
		row := make(tele.Row, 0, len(buttonRow))
		for _, button := range buttonRow {
			row = append(row, tele.Btn{Unique: callbackEndpoint, Text: button.Text, Data: button.Data})
		}
		rows = append(rows, row)
	}
	markup.Inline(rows...)
	return markup
}

func buildHTTPClient(proxyURL string) *http.Client {
	if proxyURL == "" {
		return nil
	}
	proxy, err := url.Parse(proxyURL)
	if err != nil {
		return &http.Client{}
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
}
