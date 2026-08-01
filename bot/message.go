package bot

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"
)

// minEditInterval is the minimum time between two progress edits of the same
// message. Telegram rate-limits message edits (roughly one per second and it
// returns HTTP 429 "retry after N" on bursts), so we keep a comfortable gap to
// avoid flooding and the dreaded "stuck" progress bar.
const minEditInterval = 3 * time.Second

// editState tracks the last rendered text and time for a single chat message so
// edits can be throttled and de-duplicated independently per message.
type editState struct {
	mu       sync.Mutex
	lastText string
	lastEdit time.Time
}

func (bh *BotHandler) editState(msg *tele.Message) *editState {
	key := fmt.Sprintf("%d:%d", msg.Chat.ID, msg.ID)
	v, _ := bh.edits.LoadOrStore(key, &editState{})
	return v.(*editState)
}

// cleanupEditState removes the per-message state once a task is done so the map
// does not grow unbounded over the lifetime of the process.
func (bh *BotHandler) cleanupEditState(msg *tele.Message) {
	if msg == nil {
		return
	}
	bh.edits.Delete(fmt.Sprintf("%d:%d", msg.Chat.ID, msg.ID))
}

// htmlOpts returns the standard HTML parse-mode send options.
func htmlOpts() *tele.SendOptions {
	return &tele.SendOptions{ParseMode: tele.ModeHTML}
}

// esc escapes the characters that Telegram's HTML parse mode treats specially.
// Filenames frequently contain "&", "<" or ">" (especially torrent names), and
// an unescaped one makes Telegram reject the whole edit with a 400 error, which
// previously left the progress message frozen.
func esc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// editMsg performs a throttled, de-duplicated edit. Use this for frequent
// progress updates. It silently skips edits that arrive too soon or that would
// not change the message text.
func (bh *BotHandler) editMsg(msg *tele.Message, text string, markup ...*tele.ReplyMarkup) {
	bh.doEdit(msg, text, false, markup...)
}

// editFinal forces an edit through regardless of the throttle window (but still
// skips a no-op edit if the text is identical). Use this for state transitions
// and terminal messages such as completion or failure.
func (bh *BotHandler) editFinal(msg *tele.Message, text string, markup ...*tele.ReplyMarkup) {
	bh.doEdit(msg, text, true, markup...)
}

func (bh *BotHandler) doEdit(msg *tele.Message, text string, force bool, markup ...*tele.ReplyMarkup) {
	if msg == nil {
		return
	}

	st := bh.editState(msg)
	st.mu.Lock()
	defer st.mu.Unlock()

	// Identical text would make Telegram return "message is not modified".
	if text == st.lastText {
		return
	}
	// Throttle non-forced (progress) edits.
	if !force && time.Since(st.lastEdit) < minEditInterval {
		return
	}

	opts := []interface{}{htmlOpts()}
	if len(markup) > 0 && markup[0] != nil {
		opts = append(opts, markup[0])
	}

	_, err := bh.bot.Edit(msg, text, opts...)
	if err != nil {
		// Honour Telegram's flood control and retry once.
		if wait := retryAfter(err); wait > 0 {
			time.Sleep(wait)
			_, err = bh.bot.Edit(msg, text, opts...)
		}
	}
	if err != nil {
		if isNotModified(err) {
			// Treat as success so we stop trying to re-send the same text.
			st.lastText = text
			st.lastEdit = time.Now()
			return
		}
		// Leave lastText untouched so the next tick retries the update.
		return
	}

	st.lastText = text
	st.lastEdit = time.Now()
}

// retryAfter extracts the back-off duration from a Telegram flood-control error
// (message contains "retry after N"). Returns 0 if not a flood error.
func retryAfter(err error) time.Duration {
	s := strings.ToLower(err.Error())
	idx := strings.Index(s, "retry after")
	if idx == -1 {
		return 0
	}
	rest := s[idx+len("retry after"):]
	num := strings.Builder{}
	for _, r := range rest {
		if r >= '0' && r <= '9' {
			num.WriteRune(r)
			continue
		}
		if num.Len() > 0 {
			break
		}
		if r == ' ' || r == ':' {
			continue
		}
		break
	}
	n, _ := strconv.Atoi(num.String())
	if n <= 0 {
		return 0
	}
	if n > 60 {
		n = 60 // sanity cap
	}
	return time.Duration(n) * time.Second
}

func isNotModified(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "not modified")
}

// ===== Keyboards =====

// cancelButton builds an inline keyboard with a single Cancel action carrying
// the task ID as callback data.
func cancelButton(taskID int) *tele.ReplyMarkup {
	menu := &tele.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("🚫 Cancel", "cancel_task", strconv.Itoa(taskID))),
	)
	return menu
}
