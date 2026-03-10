// Provides a way to restart the bot when settings change
package bot

import (
	"log"
	"sync"
)

type BotOrchestrator struct {
	mu           sync.Mutex
	currentToken string
	currentAPI   string
	botHandler   *BotHandler
}

// NewBotOrchestrator manages the bot lifecycle
func NewBotOrchestrator() *BotOrchestrator {
	return &BotOrchestrator{}
}

// Reload checks if the token changed and restarts the bot if necessary
func (bo *BotOrchestrator) Reload(newToken string, apiURL string) {
	bo.mu.Lock()
	defer bo.mu.Unlock()

	// If token hasn't changed or is empty, do nothing
	if newToken == bo.currentToken && apiURL == bo.currentAPI {
		return
	}
	if newToken == "" {
		return
	}

	log.Printf("Bot settings changed, restarting bot...")

	// Stop existing bot
	if bo.botHandler != nil {
		bo.botHandler.Stop()
	}

	// Start new bot
	bh, err := NewBot(newToken, apiURL)
	if err != nil {
		log.Printf("Failed to initialize bot with new token: %v", err)
		return
	}

	bo.currentToken = newToken
	bo.currentAPI = apiURL
	bo.botHandler = bh
	
	go bh.Start()
}

// Ensure the actual BotHandler has a Stop method
func (bh *BotHandler) Stop() {
	if bh.bot != nil {
		bh.bot.Stop()
	}
}
