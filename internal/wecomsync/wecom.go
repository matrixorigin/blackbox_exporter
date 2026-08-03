// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package wecomsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type WeComClient struct {
	BaseURL    string
	CorpID     string
	CorpSecret string
	DocID      string
	SheetID    string
	SheetIDs   []string
	ViewID     string
	KeyType    string
	PageSize   int
	HTTPClient *http.Client
}

func NewWeComClient(cfg Config) (*WeComClient, error) {
	corpID, corpSecret, err := cfg.WeComCredentials()
	if err != nil {
		return nil, err
	}
	return &WeComClient{
		BaseURL:    strings.TrimRight(cfg.WeCom.BaseURL, "/"),
		CorpID:     corpID,
		CorpSecret: corpSecret,
		DocID:      cfg.WeCom.DocID,
		SheetID:    cfg.WeCom.SheetID,
		SheetIDs:   cfg.WeComSheetIDs(),
		ViewID:     cfg.WeCom.ViewID,
		KeyType:    cfg.WeCom.KeyType,
		PageSize:   cfg.WeCom.PageSize,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *WeComClient) Records(ctx context.Context) ([]SheetRecord, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	sheetIDs := c.SheetIDs
	if len(sheetIDs) == 0 && strings.TrimSpace(c.SheetID) != "" {
		sheetIDs = []string{strings.TrimSpace(c.SheetID)}
	}
	var records []SheetRecord
	for _, sheetID := range sheetIDs {
		rawRecords, err := c.smartSheetRecords(ctx, token, sheetID)
		if err != nil {
			return nil, err
		}
		for _, raw := range rawRecords {
			record := NormalizeRecord(raw)
			record.SheetID = sheetID
			records = append(records, record)
		}
	}
	return records, nil
}

func (c *WeComClient) accessToken(ctx context.Context) (string, error) {
	u, err := url.Parse(c.BaseURL + "/cgi-bin/gettoken")
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("corpid", c.CorpID)
	q.Set("corpsecret", c.CorpSecret)
	u.RawQuery = q.Encode()

	var resp struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
	}
	if err := c.doJSON(ctx, http.MethodGet, u.String(), nil, &resp); err != nil {
		return "", err
	}
	if resp.ErrCode != 0 {
		return "", fmt.Errorf("wecom gettoken errcode=%d errmsg=%s", resp.ErrCode, resp.ErrMsg)
	}
	if resp.AccessToken == "" {
		return "", fmt.Errorf("wecom gettoken returned empty access_token")
	}
	return resp.AccessToken, nil
}

func (c *WeComClient) smartSheetRecords(ctx context.Context, token, sheetID string) ([]map[string]any, error) {
	var all []map[string]any
	offset := 0
	limit := c.PageSize
	if limit <= 0 {
		limit = defaultPageSize
	}
	for {
		page, hasMore, nextOffset, err := c.smartSheetRecordsPage(ctx, token, sheetID, offset, limit)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if !hasMore {
			return all, nil
		}
		if nextOffset > offset {
			offset = nextOffset
		} else {
			offset += limit
		}
	}
}

func (c *WeComClient) smartSheetRecordsPage(ctx context.Context, token, sheetID string, offset, limit int) ([]map[string]any, bool, int, error) {
	u, err := url.Parse(c.BaseURL + "/cgi-bin/wedoc/smartsheet/get_records")
	if err != nil {
		return nil, false, 0, err
	}
	q := u.Query()
	q.Set("access_token", token)
	u.RawQuery = q.Encode()

	body := map[string]any{
		"docid":    c.DocID,
		"sheet_id": sheetID,
		"key_type": c.KeyType,
		"offset":   offset,
		"limit":    limit,
	}
	if c.ViewID != "" {
		body["view_id"] = c.ViewID
	}

	var resp smartSheetResponse
	if err := c.doJSON(ctx, http.MethodPost, u.String(), body, &resp); err != nil {
		return nil, false, 0, err
	}
	if resp.ErrCode != 0 {
		return nil, false, 0, fmt.Errorf("wecom get_records errcode=%d errmsg=%s", resp.ErrCode, resp.ErrMsg)
	}
	records := resp.Records
	if len(records) == 0 && len(resp.Data.Records) > 0 {
		records = resp.Data.Records
	}
	hasMore := resp.HasMore || resp.Data.HasMore
	nextOffset := firstPositive(resp.NextOffset, resp.Next, resp.Data.NextOffset, resp.Data.Next)
	return records, hasMore, nextOffset, nil
}

type smartSheetResponse struct {
	ErrCode    int              `json:"errcode"`
	ErrMsg     string           `json:"errmsg"`
	Records    []map[string]any `json:"records"`
	HasMore    bool             `json:"has_more"`
	Next       int              `json:"next"`
	NextOffset int              `json:"next_offset"`
	Data       struct {
		Records    []map[string]any `json:"records"`
		HasMore    bool             `json:"has_more"`
		Next       int              `json:"next"`
		NextOffset int              `json:"next_offset"`
	} `json:"data"`
}

func (c *WeComClient) doJSON(ctx context.Context, method, rawURL string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("wecom API status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 10<<20))
	if err := dec.Decode(out); err != nil {
		return err
	}
	return nil
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
