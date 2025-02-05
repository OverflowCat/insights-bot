package chathistories

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"entgo.io/ent/dialect/sql"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/fx"

	"github.com/nekomeowww/insights-bot/ent"
	"github.com/nekomeowww/insights-bot/ent/chathistories"
	"github.com/nekomeowww/insights-bot/internal/datastore"
	"github.com/nekomeowww/insights-bot/pkg/bots/tgbot"
	"github.com/nekomeowww/insights-bot/pkg/logger"
	"github.com/nekomeowww/insights-bot/pkg/openai"
)

type NewModelParams struct {
	fx.In

	Logger *logger.Logger
	Ent    *datastore.Ent
	OpenAI *openai.Client
}

type Model struct {
	logger *logger.Logger
	ent    *datastore.Ent
	openAI *openai.Client
}

func NewModel() func(NewModelParams) (*Model, error) {
	return func(param NewModelParams) (*Model, error) {
		return &Model{
			logger: param.Logger,
			ent:    param.Ent,
			openAI: param.OpenAI,
		}, nil
	}
}

func (m *Model) extractTextWithSummarization(message *tgbotapi.Message) (string, error) {
	text := tgbot.ExtractTextFromMessage(message)
	if text == "" {
		return "", nil
	}
	if utf8.RuneCountInString(text) >= 200 {
		resp, err := m.openAI.SummarizeWithOneChatHistory(context.Background(), text)
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", nil
		}

		return resp.Choices[0].Message.Content, nil
	}

	return text, nil
}

func (m *Model) SaveOneTelegramChatHistory(message *tgbotapi.Message) error {
	if message.Text == "" && message.Caption == "" {
		m.logger.Warn("message text is empty")
		return nil
	}

	if strings.Contains(message.Text, "#recap") {
		m.logger.Warn("message appears to be a recap sent by an insights-bot instance")
		return nil
	}

	telegramChatHistoryCreate := m.ent.ChatHistories.
		Create().
		SetChatID(message.Chat.ID).
		SetMessageID(int64(message.MessageID)).
		SetUserID(message.From.ID).
		SetUsername(message.From.UserName).
		SetFullName(tgbot.FullNameFromFirstAndLastName(message.From.FirstName, message.From.LastName)).
		SetChattedAt(time.Unix(int64(message.Date), 0).UnixMilli())

	text, err := m.extractTextWithSummarization(message)
	if err != nil {
		return err
	}
	if text == "" {
		m.logger.Warn("message text is empty")
		return nil
	}
	if message.ForwardFrom != nil {
		telegramChatHistoryCreate.SetText("转发了来自" + tgbot.FullNameFromFirstAndLastName(message.ForwardFrom.FirstName, message.ForwardFrom.LastName) + "的消息：" + text)
	} else if message.ForwardFromChat != nil {
		telegramChatHistoryCreate.SetText("转发了来自" + message.ForwardFromChat.Title + "的消息：" + text)
	} else {
		telegramChatHistoryCreate.SetText(text)
	}
	if message.ReplyToMessage != nil {
		repliedToText, err := m.extractTextWithSummarization(message.ReplyToMessage)
		if err != nil {
			return err
		}
		if repliedToText != "" {
			telegramChatHistoryCreate.SetRepliedToMessageID(int64(message.ReplyToMessage.MessageID))
			telegramChatHistoryCreate.SetRepliedToUserID(message.ReplyToMessage.From.ID)
			telegramChatHistoryCreate.SetRepliedToFullName(tgbot.FullNameFromFirstAndLastName(message.ReplyToMessage.From.FirstName, message.ReplyToMessage.From.LastName))
			telegramChatHistoryCreate.SetRepliedToUsername(message.ReplyToMessage.From.UserName)
			telegramChatHistoryCreate.SetRepliedToText(repliedToText)
		}
	}

	telegramChatHistory, err := telegramChatHistoryCreate.Save(context.TODO())
	if err != nil {
		return err
	}

	m.logger.WithFields(logrus.Fields{
		"id":         telegramChatHistory.ID,
		"chat_id":    telegramChatHistory.ChatID,
		"message_id": telegramChatHistory.MessageID,
		"text":       strings.ReplaceAll(telegramChatHistory.Text, "\n", " "),
	}).Debug("saved one telegram chat history")

	return nil
}

func (m *Model) FindLastOneHourChatHistories(chatID int64) ([]*ent.ChatHistories, error) {
	return m.FindChatHistoriesByTimeBefore(chatID, time.Hour)
}

func (m *Model) FindLastSixHourChatHistories(chatID int64) ([]*ent.ChatHistories, error) {
	return m.FindChatHistoriesByTimeBefore(chatID, 6*time.Hour)
}

func (m *Model) FindChatHistoriesByTimeBefore(chatID int64, before time.Duration) ([]*ent.ChatHistories, error) {
	m.logger.Infof("querying chat histories for %d", chatID)

	telegramChatHistories, err := m.ent.ChatHistories.
		Query().
		Where(
			chathistories.ChatID(chatID),
			chathistories.ChattedAtGT(time.Now().Add(-before).UnixMilli()),
		).
		Order(
			chathistories.ByMessageID(sql.OrderDesc()),
		).
		All(context.TODO())
	if err != nil {
		return make([]*ent.ChatHistories, 0), err
	}

	return telegramChatHistories, nil
}

func formatFullNameAndUsername(fullName, username string) string {
	if utf8.RuneCountInString(fullName) >= 10 {
		return username
	}

	return strings.ReplaceAll(fullName, "#", "")
}

type RecapOutputTemplateInputs struct {
	ChatID string
	Recaps []*openai.ChatHistorySummarizationOutputs
}

func formatChatID(chatID int64) string {
	chatIDStr := strconv.FormatInt(chatID, 10)
	if strings.HasPrefix(chatIDStr, "-100") {
		return strings.TrimPrefix(chatIDStr, "-100")
	}

	return chatIDStr
}

var RecapOutputTemplate = lo.Must(template.
	New(uuid.New().String()).
	Funcs(template.FuncMap{
		"join":   strings.Join,
		"sub":    func(a, b int) int { return a - b },
		"add":    func(a, b int) int { return a + b },
		"escape": tgbot.EscapeHTMLSymbols,
	}).
	Parse(`{{ $chatID := .ChatID }}{{ $recapLen := len .Recaps }}{{ range $i, $r := .Recaps }}{{ if $r.SinceMsgID }}## <a href="https://t.me/c/{{ $chatID }}/{{ $r.SinceMsgID }}">{{ escape $r.TopicName }}</a>{{ else }}## {{ escape $r.TopicName }}{{ end }}
参与人：{{ join $r.ParticipantsNamesWithoutUsername "，" }}
讨论：{{ range $di, $d := $r.Discussion }}
 - {{ escape $d.Point }}{{ if len $d.CriticalMessageIDs }} {{ range $cIndex, $c := $d.CriticalMessageIDs }}<a href="https://t.me/c/{{ $chatID }}/{{ $c }}">[{{ add $cIndex 1 }}]</a>{{ if not (eq $cIndex (sub (len $d.CriticalMessageIDs) 1)) }} {{ end }}{{ end }}{{ end }}{{ end }}{{ if $r.Conclusion }}
结论：{{ escape $r.Conclusion }}{{ end }}{{ if eq $i (sub $recapLen 1) }}{{ else }}

{{ end }}{{ end }}`))

func (m *Model) summarizeChatHistoriesSlice(s string) ([]*openai.ChatHistorySummarizationOutputs, error) {
	m.logger.Infof("✍️ summarizing last one hour chat histories:\n%s", s)

	resp, err := m.openAI.SummarizeWithChatHistories(context.Background(), s)
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, nil
	}

	m.logger.WithFields(logrus.Fields{
		"prompt_token_usage":     resp.Usage.PromptTokens,
		"completion_token_usage": resp.Usage.CompletionTokens,
		"total_token_usage":      resp.Usage.TotalTokens,
	}).Info("✅ summarized last one hour chat histories")
	if resp.Choices[0].Message.Content == "" {
		return nil, nil
	}

	var outputs []*openai.ChatHistorySummarizationOutputs

	err = json.Unmarshal([]byte(resp.Choices[0].Message.Content), &outputs)
	if err != nil {
		m.logger.Errorf("failed to unmarshal chat history summarization output: %s", resp.Choices[0].Message.Content)
		return nil, err
	}

	return outputs, nil
}

func (m *Model) SummarizeChatHistories(chatID int64, histories []*ent.ChatHistories) (string, error) {
	historiesLLMFriendly := make([]string, 0, len(histories))

	for _, message := range histories {
		if message.RepliedToMessageID == 0 {
			historiesLLMFriendly = append(historiesLLMFriendly, fmt.Sprintf(
				"msgId:%d: %s 发送：%s",
				message.MessageID,
				formatFullNameAndUsername(message.FullName, message.Username),
				message.Text,
			))
		} else {
			repliedToPartialContextMessage := fmt.Sprintf("%s 发送的 msgId:%d 的消息", formatFullNameAndUsername(message.RepliedToFullName, message.RepliedToUsername), message.RepliedToMessageID)
			historiesLLMFriendly = append(historiesLLMFriendly, fmt.Sprintf(
				"msgId:%d: %s 回复 %s：%s",
				message.MessageID,
				formatFullNameAndUsername(message.FullName, message.Username),
				repliedToPartialContextMessage,
				message.Text,
			))
		}
	}

	chatHistories := strings.Join(historiesLLMFriendly, "\n")
	chatHistoriesSlices := m.openAI.SplitContentBasedByTokenLimitations(chatHistories, 2800)
	chatHistoriesSummarizations := make([]*openai.ChatHistorySummarizationOutputs, 0, len(chatHistoriesSlices))

	for _, s := range chatHistoriesSlices {
		var outputs []*openai.ChatHistorySummarizationOutputs

		_, _, err := lo.AttemptWithDelay(3, time.Second, func(tried int, delay time.Duration) error {
			o, err := m.summarizeChatHistoriesSlice(s)
			if err != nil {
				m.logger.Errorf("failed to summarize chat histories slice: %s, tried %d...", s, tried)
				return err
			}

			outputs = o
			return nil
		})
		if err != nil {
			return "", err
		}
		if outputs == nil {
			continue
		}

		for _, o := range outputs {
			for _, d := range o.Discussion {
				d.CriticalMessageIDs = lo.UniqBy(d.CriticalMessageIDs, func(item int64) int64 {
					return item
				})
				d.CriticalMessageIDs = lo.Filter(d.CriticalMessageIDs, func(item int64, _ int) bool {
					return item != 0
				})

				if len(d.CriticalMessageIDs) > 5 {
					d.CriticalMessageIDs = d.CriticalMessageIDs[:5]
				}
			}
		}

		chatHistoriesSummarizations = append(chatHistoriesSummarizations, outputs...)
	}

	sb := new(strings.Builder)

	err := RecapOutputTemplate.Execute(sb, RecapOutputTemplateInputs{
		ChatID: formatChatID(chatID),
		Recaps: chatHistoriesSummarizations,
	})
	if err != nil {
		return "", err
	}

	return sb.String(), nil
}
