package tk2sd

import (
	"context"
	"net/http"
	"strconv"
)

// lease 只是兼容指针；分页读取全部活动ID才能去除自有任务的重复占用。
// 网络读取在监控阶段执行，不持有数据库领取事务。
func (c *Client) activeTaskIDs(ctx context.Context) ([]string, error) {
	const pageSize, maxPages = 100, 100
	ids := []string{}
	seen := map[string]bool{}
	total := -1
	for page := 1; page <= maxPages; page++ {
		r, err := c.newRequest(ctx, http.MethodGet, tasksPath, nil, "")
		if err != nil {
			return nil, err
		}
		q := r.URL.Query()
		q.Set("filter.status", "running")
		q.Set("page_num", strconv.Itoa(page))
		q.Set("page_size", strconv.Itoa(pageSize))
		r.URL.RawQuery = q.Encode()
		response, err := c.do(r)
		if err != nil {
			return nil, err
		}
		var result struct {
			Total *int `json:"total"`
			Items *[]struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"items"`
		}
		err = c.decode(response, http.StatusOK, &result, false)
		_ = response.Body.Close()
		if err != nil {
			return nil, err
		}
		if result.Total == nil || result.Items == nil || *result.Total < 0 || *result.Total > maxPages*pageSize {
			return nil, ErrInvalidResponse
		}
		if total == -1 {
			total = *result.Total
		}
		if total != *result.Total || len(*result.Items) != min(pageSize, total-len(ids)) {
			return nil, ErrInvalidResponse
		}
		for _, item := range *result.Items {
			if !validID(item.ID) || seen[item.ID] || item.Status != "running" {
				return nil, ErrInvalidResponse
			}
			seen[item.ID] = true
			ids = append(ids, item.ID)
		}
		if len(ids) == total {
			return ids, nil
		}
	}
	return nil, ErrInvalidResponse
}
