// Package gateway connects a session to a chat service, so a run can be
// driven and watched from somewhere other than the terminal.
//
// Long-polling rather than webhooks: a webhook needs a public URL, which a
// program running on someone's laptop does not have. Polling costs one idle
// HTTP request at a time and needs no infrastructure at all.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// pollTimeout is how long Telegram holds an empty request open. Long, because
// a held request is free and a short one is a busy loop.
const pollTimeout = 50 * time.Second

// maxMessage is Telegram's own limit; a longer reply is split rather than
// rejected.
const maxMessage = 4000

// Telegram is a bot connection.
type Telegram struct {
	token  string
	chatID int64
	client *http.Client
	offset int64
}

// NewTelegram builds a connection. An empty token disables it.
func NewTelegram(token string, chatID int64) *Telegram {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	return &Telegram{
		token:  token,
		chatID: chatID,
		// Longer than the poll, or every long-poll would look like a timeout.
		client: &http.Client{Timeout: pollTimeout + 15*time.Second},
	}
}

// Message is one inbound message.
type Message struct {
	ChatID int64
	From   string
	Text   string
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		From struct {
			Username string `json:"username"`
		} `json:"from"`
		Text string `json:"text"`
	} `json:"message"`
}

// Poll waits for the next batch of messages.
//
// Only messages from the configured chat are returned. A bot token is a
// bearer credential — anyone who learns the bot's name can message it — so
// without this check a stranger could drive the agent.
func (t *Telegram) Poll(ctx context.Context) ([]Message, error) {
	params := url.Values{
		"timeout": {fmt.Sprint(int(pollTimeout.Seconds()))},
		"offset":  {fmt.Sprint(t.offset)},
	}
	raw, err := t.call(ctx, "getUpdates", params)
	if err != nil {
		return nil, err
	}
	var updates []update
	if err := json.Unmarshal(raw, &updates); err != nil {
		return nil, fmt.Errorf("telegram: malformed updates: %w", err)
	}

	var out []Message
	for _, u := range updates {
		// Acknowledge every update, including ones being ignored: an
		// unacknowledged update is redelivered forever.
		if u.UpdateID >= t.offset {
			t.offset = u.UpdateID + 1
		}
		if u.Message == nil || strings.TrimSpace(u.Message.Text) == "" {
			continue
		}
		if t.chatID != 0 && u.Message.Chat.ID != t.chatID {
			continue
		}
		out = append(out, Message{
			ChatID: u.Message.Chat.ID,
			From:   u.Message.From.Username,
			Text:   u.Message.Text,
		})
	}
	return out, nil
}

// Send posts a reply, splitting anything past the service's limit.
func (t *Telegram) Send(ctx context.Context, chatID int64, text string) error {
	if chatID == 0 {
		chatID = t.chatID
	}
	for _, chunk := range split(text, maxMessage) {
		params := url.Values{
			"chat_id": {fmt.Sprint(chatID)},
			"text":    {chunk},
		}
		if _, err := t.call(ctx, "sendMessage", params); err != nil {
			return err
		}
	}
	return nil
}

// split cuts text into chunks, preferring line boundaries so a code block or
// a list is not severed mid-line.
func split(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}
	var out []string
	for len(text) > limit {
		cut := strings.LastIndex(text[:limit], "\n")
		if cut <= 0 {
			cut = limit
		}
		out = append(out, text[:cut])
		text = strings.TrimPrefix(text[cut:], "\n")
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

// call performs one API request.
func (t *Telegram) call(ctx context.Context, method string, params url.Values) (json.RawMessage, error) {
	endpoint := "https://api.telegram.org/bot" + t.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var api apiResponse
	if err := json.Unmarshal(body, &api); err != nil {
		return nil, fmt.Errorf("telegram: %s returned unparseable JSON", method)
	}
	if !api.OK {
		// The description is the only useful part of a Telegram failure.
		return nil, fmt.Errorf("telegram: %s: %s", method, api.Description)
	}
	return api.Result, nil
}
