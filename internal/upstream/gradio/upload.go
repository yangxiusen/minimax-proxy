package gradio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/httpapi/v2"
)

func (c *Client) PrepareArguments(ctx context.Context, request v2.ValidatedRequest, profile config.GenerationProfile) ([]any, error) {
	prepared := request
	prepared.Content = append([]v2.ContentItem(nil), request.Content...)
	for index := range prepared.Content {
		item := &prepared.Content[index]
		if item.AudioURL == nil {
			continue
		}
		mediaType, content, isDataURI, err := v2.ParseAudioDataURI(item.AudioURL.URL)
		if err != nil {
			return nil, err
		}
		if !isDataURI {
			continue
		}
		extension := ".mp3"
		if mediaType == "audio/wav" {
			extension = ".wav"
		}
		uploadedPath, err := c.upload(ctx, fmt.Sprintf("reference-audio-%d%s", index, extension), content)
		if err != nil {
			return nil, fmt.Errorf("上传参考音频: %w", err)
		}
		audioURL := *item.AudioURL
		audioURL.URL = uploadedPath
		item.AudioURL = &audioURL
	}
	return BuildArguments(prepared, profile)
}

func (c *Client) upload(ctx context.Context, filename string, content []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(content); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("gradio_api", "upload").String(), &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("Gradio upload POST: %w", err)
	}
	var paths []string
	if err := decodeLimited(response, c.maxBody, &paths); err != nil {
		return "", fmt.Errorf("Gradio upload 响应: %w", err)
	}
	if len(paths) != 1 || paths[0] == "" {
		return "", errors.New("Gradio upload 未返回唯一文件路径")
	}
	return paths[0], nil
}
