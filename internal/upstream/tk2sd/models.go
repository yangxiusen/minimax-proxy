package tk2sd

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
)

type Model struct {
	ID        string `json:"id"`
	Mode      string `json:"mode"`
	Durations []int  `json:"durations"`
}

func (c *Client) Models(ctx context.Context) ([]Model, error) {
	var result struct {
		Data []Model `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "/v1/models", &result, false); err != nil {
		return nil, err
	}
	if result.Data == nil {
		return nil, ErrInvalidResponse
	}
	ids := make(map[string]bool)
	seen := make(map[[2]string][]int)
	models := make([]Model, 0, len(result.Data))
	for _, model := range result.Data {
		if !validName(model.ID) || len(model.Durations) == 0 {
			return nil, ErrInvalidResponse
		}
		switch model.Mode {
		case "text_to_video", "image_to_video", "reference_to_video":
		default:
			return nil, ErrInvalidResponse
		}
		slices.Sort(model.Durations)
		for i, d := range model.Durations {
			if d <= 0 || (i > 0 && d == model.Durations[i-1]) {
				return nil, ErrInvalidResponse
			}
		}
		key := [2]string{model.ID, model.Mode}
		if prev, ok := seen[key]; ok {
			if !slices.Equal(prev, model.Durations) {
				return nil, ErrInvalidResponse
			}
			continue
		}
		seen[key] = model.Durations
		ids[model.ID] = true
		if len(ids) > 256 {
			return nil, ErrInvalidResponse
		}
		models = append(models, model)
	}
	return models, nil
}

func (c *Client) Health(ctx context.Context) error {
	r, err := c.newRequest(ctx, http.MethodGet, tasksPath, nil, "")
	if err != nil {
		return err
	}
	r.URL.RawQuery = "page_num=1&page_size=1"
	response, err := c.do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var result struct {
		Total *int              `json:"total"`
		Items []json.RawMessage `json:"items"`
	}
	if err := c.decode(response, http.StatusOK, &result, false); err != nil {
		return err
	}
	if result.Total == nil || *result.Total < 0 || result.Items == nil || len(result.Items) > 1 || len(result.Items) > *result.Total {
		return ErrInvalidResponse
	}
	return nil
}
