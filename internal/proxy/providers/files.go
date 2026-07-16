package providers

import (
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/Korbicorp/klovys99/internal/fileanonymizer"
)

type FileAnonymizer interface {
	Enabled() bool
	FailurePolicy() string
	Anonymize(context.Context, string, string, []byte) ([]byte, anonymizer.Result, error)
	AnonymizePlainText([]byte) ([]byte, anonymizer.Result)
	RegisterFileID(string, string)
	IsTrustedFileID(string, string) bool
}

type fileOutcome struct {
	stats    map[anonymizer.EntityType]int
	findings []anonymizer.Finding
}

func anonymizeInlineFiles(ctx context.Context, provider string, processor FileAnonymizer, value any) (bool, fileOutcome, error) {
	outcome := fileOutcome{stats: make(map[anonymizer.EntityType]int)}
	if processor == nil || !processor.Enabled() {
		return false, outcome, nil
	}
	changed, keep, _, err := walkFileValue(ctx, provider, processor, value, &outcome)
	if !keep && err == nil {
		err = fmt.Errorf("request contains no usable content after file removal")
	}
	return changed, outcome, err
}

func walkFileValue(ctx context.Context, provider string, processor FileAnonymizer, value any, outcome *fileOutcome) (bool, bool, any, error) {
	switch typed := value.(type) {
	case []any:
		changed := false
		kept := make([]any, 0, len(typed))
		for _, item := range typed {
			itemChanged, keep, next, err := walkFileValue(ctx, provider, processor, item, outcome)
			if err != nil {
				return false, false, value, err
			}
			changed = changed || itemChanged || !keep
			if keep {
				kept = append(kept, next)
			}
		}
		return changed, len(kept) > 0, kept, nil
	case map[string]any:
		if isFileBlock(typed) {
			changed, keep, err := processFileBlock(ctx, provider, processor, typed, outcome)
			return changed, keep, typed, err
		}
		changed := false
		for key, item := range typed {
			itemChanged, keep, next, err := walkFileValue(ctx, provider, processor, item, outcome)
			if err != nil {
				return false, false, value, err
			}
			changed = changed || itemChanged
			if !keep {
				delete(typed, key)
				changed = true
			} else {
				typed[key] = next
			}
		}
		return changed, len(typed) > 0, typed, nil
	default:
		return false, true, value, nil
	}
}

func isFileBlock(v map[string]any) bool {
	t := stringMapValue(v, "type")
	if t == "input_file" || t == "input_image" || t == "image_url" || t == "image" || t == "document" {
		return true
	}
	_, source := v["source"].(map[string]any)
	return source && (t == "image" || t == "document")
}

func processFileBlock(ctx context.Context, provider string, processor FileAnonymizer, block map[string]any, outcome *fileOutcome) (bool, bool, error) {
	target := block
	if source, ok := block["source"].(map[string]any); ok {
		target = source
	}
	if imageURL, ok := block["image_url"].(map[string]any); ok {
		target = imageURL
	}
	mediaType := stringMapValue(target, "media_type")
	if id := stringMapValue(target, "file_id"); id != "" {
		if processor.IsTrustedFileID(provider, id) {
			return false, true, nil
		}
		return fileFailure(processor, fmt.Errorf("unverified file reference"))
	}
	var key, encoded string
	for _, candidate := range []string{"file_data", "data", "image_url", "url"} {
		if s, ok := target[candidate].(string); ok {
			key, encoded = candidate, s
			break
		}
	}
	if encoded == "" {
		return fileFailure(processor, fmt.Errorf("unverified file reference"))
	}
	data := []byte(nil)
	dataURL := false
	if mt, decoded, ok := fileanonymizer.DecodeDataURL(encoded); ok {
		mediaType, data, dataURL = mt, decoded, true
	} else {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return fileFailure(processor, fmt.Errorf("invalid inline file data"))
		}
		data = decoded
	}
	if mediaType == "" {
		if name := stringMapValue(block, "filename"); name != "" {
			mediaType = mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
		}
		if mediaType == "" {
			mediaType = http.DetectContentType(data)
		}
	}
	var output []byte
	var result anonymizer.Result
	var err error
	if strings.HasPrefix(mediaType, "text/") {
		output, result = processor.AnonymizePlainText(data)
	} else {
		output, result, err = processor.Anonymize(ctx, provider, mediaType, data)
	}
	if err != nil {
		return fileFailure(processor, err)
	}
	if dataURL {
		target[key] = fileanonymizer.EncodeDataURL(mediaType, output)
	} else {
		target[key] = base64.StdEncoding.EncodeToString(output)
	}
	for typ, stat := range result.Stats {
		outcome.stats[typ] += stat.Count
	}
	outcome.findings = append(outcome.findings, result.Findings...)
	return true, true, nil
}

func fileFailure(processor FileAnonymizer, err error) (bool, bool, error) {
	switch processor.FailurePolicy() {
	case fileanonymizer.PolicyPassthrough:
		return false, true, nil
	case fileanonymizer.PolicyRemove:
		return true, false, nil
	default:
		return false, false, err
	}
}
