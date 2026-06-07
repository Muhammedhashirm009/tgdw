package bot

import (
	"strconv"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"
)

// editState tracks the last rendered text for a single chat message so edits
// can be de-duplicated and serialized independently per message.
type editState struct {
	mu       sync.Mutex
	lastText string
}

func (bh *BotHandler) editState(msg *tele.Message) *editState {
	key := strconv.FormatInt(msg.Chat.ID, 10) + ":" + strconv.Itoa(msg.ID)
	v, _ := bh.edits.LoadOrStore(key, &editState{})
	return v.(*editState)
}

// cleanupEditState removes the per-message state once a task is done so the map
// does not grow unbounded over the lifetime of the process.
func (bh *BotHandler) cleanupEditState(msg *tele.Message) {
	if msg == nil {
		return
	}
	bh.edits.Delete(strconv.FormatInt(msg.Chat.ID, 10) + ":" + strconv.Itoa(msg.ID))
}

// edit is a safe replacement for bh.bot.Edit. It:
//   - serializes concurrent edits to the same message (per-message mutex), which
//     matters because progress callbacks can fire from several goroutines;
//   - skips edits whose text is identical to the last one (Telegram otherwise
//     returns 400 "message is not modified");
//   - honours Telegram flood-control (HTTP 429 "retry after N") with one retry.
//
// Callers already throttle how often they call edit (every 3-4s), so no extra
// time-based throttle is applied here. The variadic opts are passed straight
// through to telebot (ParseMode, reply markup, etc.).
func (bh *BotHandler) edit(msg *tele.Message, what string, opts ...interface{}) {
	if msg == nil {
		return
	}

	st := bh.editState(msg)
	st.mu.Lock()
	defer st.mu.Unlock()

	if what == st.lastText {
		return
	}

	_, err := bh.bot.Edit(msg, what, opts...)
	if err != nil {
		if wait := retryAfter(err); wait > 0 {
			time.Sleep(wait)
			_, err = bh.bot.Edit(msg, what, opts...)
		}
		if err != nil && !isNotModified(err) {
			// Leave lastText unchanged so a later call retries the update.
			return
		}
	}

	st.lastText = what
}

// retryAfter extracts the back-off duration from a Telegram flood-control error
// (the message contains "retry after N"). Returns 0 if it isn't a flood error.
func retryAfter(err error) time.Duration {
	s := strings.ToLower(err.Error())
	idx := strings.Index(s, "retry after")
	if idx == -1 {
		return 0
	}
	num := strings.Builder{}
	for _, r := range s[idx+len("retry after"):] {
		if r >= '0' && r <= '9' {
			num.WriteRune(r)
			continue
		}
		if num.Len() > 0 {
			break
		}
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

// esc escapes the characters Telegram's HTML parse mode treats specially.
// Filenames frequently contain "&", "<" or ">"; an unescaped one makes Telegram
// reject the whole edit with a 400 error, freezing the progress message.
func esc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
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
