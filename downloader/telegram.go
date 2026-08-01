package downloader

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
)

// TelegramFileDownloader handles Telegram file downloads via MTProto (no 20MB limit)
type TelegramFileDownloader struct {
	BotToken string
	ApiID    int
	ApiHash  string
}

// NewTelegramFileDownloader creates a downloader using MTProto credentials
func NewTelegramFileDownloader(botToken, apiIDStr, apiHash string) (*TelegramFileDownloader, error) {
	apiID, err := strconv.Atoi(apiIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid TELEGRAM_API_ID: %w", err)
	}
	return &TelegramFileDownloader{
		BotToken: botToken,
		ApiID:    apiID,
		ApiHash:  apiHash,
	}, nil
}

// DownloadByFileID downloads a Telegram file using Bot API file_id via MTProto
func (tfd *TelegramFileDownloader) DownloadByFileID(ctx context.Context, fileID, destDir, fileName string, callback ProgressCallback) (string, error) {
	loc, dcID, err := decodeBotFileID(fileID)
	if err != nil {
		return "", fmt.Errorf("decode file_id: %w", err)
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}
	destPath := filepath.Join(destDir, fileName)

	log.Printf("📲 MTProto download: dc=%d, dest=%s", dcID, destPath)

	client := telegram.NewClient(tfd.ApiID, tfd.ApiHash, telegram.Options{})

	dlErr := client.Run(ctx, func(ctx context.Context) error {
		if _, err := client.Auth().Bot(ctx, tfd.BotToken); err != nil {
			return fmt.Errorf("bot auth: %w", err)
		}
		log.Println("✅ MTProto bot authenticated")

		f, err := os.Create(destPath)
		if err != nil {
			return err
		}
		defer f.Close()

		pw := &tgProgressWriter{w: f, callback: callback}

		d := downloader.NewDownloader()
		_, err = d.Download(client.API(), loc).Stream(ctx, pw)
		return err
	})

	if dlErr != nil {
		os.Remove(destPath)
		return "", dlErr
	}

	return destPath, nil
}

// ===== Bot API file_id decoder =====

// rleDecodeBytes performs Telegram's RLE decoding
// Encoding: 0x00 is stored as 0x00 + count_byte
func rleDecodeBytes(data []byte) []byte {
	var result []byte
	for i := 0; i < len(data); i++ {
		if data[i] == 0 {
			i++
			if i < len(data) {
				count := int(data[i])
				for j := 0; j < count; j++ {
					result = append(result, 0)
				}
			}
		} else {
			result = append(result, data[i])
		}
	}
	return result
}

// decodeBotFileID decodes a Telegram Bot API file_id into MTProto InputFileLocation + DC ID
func decodeBotFileID(fileID string) (tg.InputFileLocationClass, int, error) {
	// 1. Add base64 padding
	raw := strings.TrimRight(fileID, "=")
	if pad := 4 - len(raw)%4; pad < 4 {
		raw += strings.Repeat("=", pad)
	}

	// 2. Base64url decode (Telegram uses URL-safe base64)
	decoded, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		// Fallback: try standard base64
		decoded, err = base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, 0, fmt.Errorf("base64 decode: %w", err)
		}
	}

	// 3. RLE decode
	decoded = rleDecodeBytes(decoded)

	// 4. Strip version bytes (last 2 bytes: major + minor version)
	if len(decoded) >= 2 {
		decoded = decoded[:len(decoded)-2]
	}

	if len(decoded) < 8 {
		return nil, 0, fmt.Errorf("decoded data too short: %d bytes", len(decoded))
	}

	// 5. Parse file type (int32 LE) and DC ID (int32 LE)
	fileType := binary.LittleEndian.Uint32(decoded[0:4])
	dcID := int(binary.LittleEndian.Uint32(decoded[4:8]))

	typeID := int(fileType & 0x00FFFFFF)
	hasFileRef := (fileType & (1 << 25)) != 0

	offset := 8

	// 6. Read file_reference if flag is set
	var fileRef []byte
	if hasFileRef {
		if offset+4 > len(decoded) {
			return nil, 0, fmt.Errorf("data too short for file_reference length")
		}
		refLen := int(binary.LittleEndian.Uint32(decoded[offset : offset+4]))
		offset += 4
		if refLen > 0 && offset+refLen <= len(decoded) {
			fileRef = make([]byte, refLen)
			copy(fileRef, decoded[offset:offset+refLen])
			offset += refLen
		}
	}

	// 7. Parse type-specific data
	// Document-like types: 3(Voice), 4(Video), 5(Document), 10(Sticker),
	// 12(Audio), 13(Animation), 16(Wallpaper), 17(VideoNote), 21(DocumentAsFile)
	switch typeID {
	case 2: // Photo
		if offset+16 > len(decoded) {
			return nil, 0, fmt.Errorf("photo data too short")
		}
		id := int64(binary.LittleEndian.Uint64(decoded[offset : offset+8]))
		accessHash := int64(binary.LittleEndian.Uint64(decoded[offset+8 : offset+16]))
		return &tg.InputPhotoFileLocation{
			ID:            id,
			AccessHash:    accessHash,
			FileReference: fileRef,
			ThumbSize:     "y",
		}, dcID, nil

	case 3, 4, 5, 10, 12, 13, 16, 17, 21:
		if offset+16 > len(decoded) {
			return nil, 0, fmt.Errorf("document data too short: need %d more bytes, have %d", 16, len(decoded)-offset)
		}
		id := int64(binary.LittleEndian.Uint64(decoded[offset : offset+8]))
		accessHash := int64(binary.LittleEndian.Uint64(decoded[offset+8 : offset+16]))

		log.Printf("📄 Decoded file_id: type=%d dc=%d id=%d", typeID, dcID, id)

		return &tg.InputDocumentFileLocation{
			ID:            id,
			AccessHash:    accessHash,
			FileReference: fileRef,
			ThumbSize:     "",
		}, dcID, nil

	default:
		return nil, 0, fmt.Errorf("unsupported file type: %d", typeID)
	}
}

// ===== Progress writer =====

type tgProgressWriter struct {
	w          io.Writer
	downloaded int64
	total      int64
	callback   ProgressCallback
	lastReport time.Time
	lastBytes  int64
}

func (pw *tgProgressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	pw.downloaded += int64(n)

	now := time.Now()
	if pw.callback != nil && now.Sub(pw.lastReport) >= time.Second {
		elapsed := now.Sub(pw.lastReport).Seconds()
		speed := int64(0)
		if elapsed > 0 {
			speed = int64(float64(pw.downloaded-pw.lastBytes) / elapsed)
		}
		pw.callback(pw.downloaded, pw.total, speed)
		pw.lastReport = now
		pw.lastBytes = pw.downloaded
	}

	return n, err
}
