package providers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

func anonymizeMultipartUpload(ctx context.Context, provider, contentType string, body []byte, processor FileAnonymizer) ([]byte, string, error) {
	if processor == nil || !processor.Enabled() {
		return body, contentType, nil
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return nil, "", fmt.Errorf("invalid multipart upload")
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var output bytes.Buffer
	writer := multipart.NewWriter(&output)
	found := false
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nil, "", nextErr
		}
		data, readErr := io.ReadAll(part)
		if readErr != nil {
			return nil, "", readErr
		}
		header := cloneMIMEHeader(part.Header)
		if part.FormName() == "file" {
			found = true
			mediaType := part.Header.Get("Content-Type")
			if mediaType == "" {
				mediaType = http.DetectContentType(data)
			}
			processed, _, processErr := processor.Anonymize(ctx, provider, mediaType, data)
			if processErr != nil {
				switch processor.FailurePolicy() {
				case "passthrough":
					processed = data
				case "remove":
					continue
				default:
					return nil, "", processErr
				}
			}
			data = processed
		}
		destination, createErr := writer.CreatePart(header)
		if createErr != nil {
			return nil, "", createErr
		}
		if _, createErr = destination.Write(data); createErr != nil {
			return nil, "", createErr
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	if !found || !multipartContainsFile(output.Bytes(), writer.Boundary()) {
		return nil, "", fmt.Errorf("upload contains no usable file after Presidio policy")
	}
	return output.Bytes(), writer.FormDataContentType(), nil
}

func multipartContainsFile(body []byte, boundary string) bool {
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if err != nil {
			return false
		}
		if strings.EqualFold(part.FormName(), "file") {
			return true
		}
	}
}

func cloneMIMEHeader(source textproto.MIMEHeader) textproto.MIMEHeader {
	result := make(textproto.MIMEHeader, len(source))
	for key, values := range source {
		result[key] = append([]string(nil), values...)
	}
	return result
}
