package summarize

import (
	"strings"

	"github.com/nekomeowww/insights-bot/pkg/bots/tgbot"
)

func (h *Handlers) HandleChannelPost(c *tgbot.Context) (tgbot.Response, error) {
	// 转发的消息不处理
	if c.Update.ChannelPost.ForwardFrom != nil {
		return nil, nil
	}
	// 转发的消息不处理
	if c.Update.ChannelPost.ForwardFromChat != nil {
		return nil, nil
	}
	// 若无 /s 命令则不处理
	if !strings.HasPrefix(c.Update.ChannelPost.Text, "/smr ") {
		return nil, nil
	}

	urlString := strings.TrimSpace(strings.TrimPrefix(c.Update.ChannelPost.Text, "/smr "))

	summarization, err := h.smr.SummarizeInputURL(urlString)
	if err != nil {
		return nil, tgbot.NewExceptionError(err)
	}

	return c.NewEditMessageText(c.Update.ChannelPost.MessageID, summarization.FormatSummarizationAsHTML()).WithParseModeHTML(), nil
}
