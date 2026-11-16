package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

func robotRequest(c notificationChannel, message string, now time.Time) (string, []byte, error) {
	if err := validateChannel(&c); err != nil {
		return "", nil, err
	}
	if len(message) > 1800 {
		return "", nil, errors.New("通知内容过长")
	}
	endpoint := c.Webhook
	payload := map[string]any{}
	if c.Provider == "feishu" {
		payload["msg_type"] = "text"
		payload["content"] = map[string]string{"text": message}
		if c.Secret != "" {
			timestamp := strconv.FormatInt(now.Unix(), 10)
			mac := hmac.New(sha256.New, []byte(timestamp+"\n"+c.Secret))
			payload["timestamp"], payload["sign"] = timestamp, base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
	} else {
		payload["msgtype"] = "text"
		payload["text"] = map[string]string{"content": message}
		if c.Provider == "dingtalk" && c.Secret != "" {
			timestamp := strconv.FormatInt(now.UnixMilli(), 10)
			mac := hmac.New(sha256.New, []byte(c.Secret))
			_, _ = mac.Write([]byte(timestamp + "\n" + c.Secret))
			u, _ := url.Parse(endpoint)
			q := u.Query()
			q.Set("timestamp", timestamp)
			q.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
			u.RawQuery = q.Encode()
			endpoint = u.String()
		}
	}
	body, err := json.Marshal(payload)
	return endpoint, body, err
}

func (a *app) deliverNotification(ctx context.Context, c notificationChannel, message string, now time.Time) error {
	endpoint, body, err := robotRequest(c, message, now)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("通知请求创建失败")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := a.notifyClient.Do(req)
	if err != nil {
		return errors.New("连接机器人失败或超时")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("机器人返回 HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32769))
	if err != nil || len(raw) > 32768 {
		return errors.New("机器人响应读取失败")
	}
	var reply struct {
		Code       *int `json:"code"`
		ErrCode    *int `json:"errcode"`
		StatusCode *int `json:"StatusCode"`
	}
	if json.Unmarshal(raw, &reply) != nil {
		return errors.New("机器人响应不是有效 JSON")
	}
	code := reply.ErrCode
	if c.Provider == "feishu" {
		code = reply.Code
		if code == nil {
			code = reply.StatusCode
		}
	}
	if code == nil {
		return errors.New("机器人响应缺少成功状态码")
	}
	if *code != 0 {
		return notificationError(*code)
	}
	return nil
}
