package aiworkspace

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const attachmentStatusAnonymized = "anonymized"
const attachmentStatusRemoved = "removed"
const attachmentStatusPassthrough = "passthrough"

type MessageAttachment struct {
	ID        string `json:"id,omitempty"`
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	Status    string `json:"status"`
	Reusable  bool   `json:"reusable"`
	Text      string `json:"text,omitempty"`
	Warning   string `json:"warning,omitempty"`
	Data      []byte `json:"-"`
}

type UploadedAttachment struct {
	Filename  string
	MediaType string
	Data      []byte
}

func (s *Service) attachmentPath(id string) string {
	return filepath.Join(s.stateDir, "attachments", id)
}

func (s *Service) saveAttachment(attachment *MessageAttachment) error {
	if attachment == nil || !attachment.Reusable || len(attachment.Data) == 0 {
		return nil
	}
	if attachment.ID == "" {
		attachment.ID = uuid.NewString()
	}
	path := s.attachmentPath(attachment.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, attachment.Data, 0o600)
}

func (s *Service) loadAttachmentData(attachment *MessageAttachment) error {
	if attachment == nil || len(attachment.Data) > 0 || !attachment.Reusable || attachment.ID == "" {
		return nil
	}
	data, err := os.ReadFile(s.attachmentPath(attachment.ID))
	if err != nil {
		return err
	}
	attachment.Data = data
	return nil
}

func attachmentFallbackText(attachment *MessageAttachment) string {
	if attachment == nil {
		return ""
	}
	if strings.TrimSpace(attachment.Text) != "" {
		return fmt.Sprintf("Attached file %q (anonymized content):\n%s", attachment.Filename, attachment.Text)
	}
	if len(attachment.Data) > 0 {
		return fmt.Sprintf("Attached file %q (%s), encoded data URL:\ndata:%s;base64,%s", attachment.Filename, attachment.MediaType, attachment.MediaType, base64.StdEncoding.EncodeToString(attachment.Data))
	}
	return ""
}

func messageTextWithFallback(message ConversationMessage) string {
	parts := make([]string, 0, 2)
	if strings.TrimSpace(message.Content) != "" {
		parts = append(parts, message.Content)
	}
	if fallback := attachmentFallbackText(message.Attachment); fallback != "" {
		parts = append(parts, fallback)
	}
	if len(parts) == 0 && message.Role == "user" {
		return "Please analyze the attached file."
	}
	return strings.Join(parts, "\n\n")
}

func supportsNativeDocument(provider, mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch provider {
	case "claude":
		return mediaType == "application/pdf" || strings.HasPrefix(mediaType, "image/")
	case "gemini":
		return mediaType == "application/pdf" || strings.HasPrefix(mediaType, "image/")
	case "openai":
		return strings.HasPrefix(mediaType, "image/") || mediaType == "application/pdf" ||
			mediaType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" ||
			mediaType == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" ||
			mediaType == "application/vnd.ms-excel"
	default:
		return false
	}
}

func attachmentDataURL(attachment *MessageAttachment) string {
	if attachment == nil || len(attachment.Data) == 0 {
		return ""
	}
	return "data:" + attachment.MediaType + ";base64," + base64.StdEncoding.EncodeToString(attachment.Data)
}
