package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// Ref는 payload의 {id, name}이다.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Payload는 generic webhook 본문이다.
type Payload struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Message      string    `json:"message"`
	Project      Ref       `json:"project"`
	Environment  *Ref      `json:"environment"`
	Service      *Ref      `json:"service"`
	DeploymentID *string   `json:"deployment_id"`
	CreatedAt    time.Time `json:"created_at"`
	Actor        *string   `json:"actor"`
}

// Text는 채팅/이메일용 한 줄 요약이다.
func (p *Payload) Text() string {
	s := "[" + p.Project.Name + "] " + p.Kind + ": " + p.Message
	return s
}

// Sign은 body의 HMAC-SHA256 서명 헤더 값("sha256=<hex>")이다.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// Verify는 Sign의 상수 시간 검증이다(수신 측 예시·테스트용).
func Verify(secret string, body []byte, header string) bool {
	return hmac.Equal([]byte(Sign(secret, body)), []byte(header))
}

// Body는 채널 종류에 맞는 본문이다: webhook은 Payload JSON, slack은 {text}, discord는 {content}.
func Body(kind string, p *Payload) []byte {
	var v any
	switch kind {
	case store.ChannelSlack:
		v = map[string]string{"text": slackEscape(p.Text())}
	case store.ChannelDiscord:
		// allowed_mentions를 비워 메시지 안의 @everyone, <@id> 같은 글이 실제 멘션이 되지 않게 한다.
		v = map[string]any{"content": truncate(p.Text(), 1900), "allowed_mentions": map[string][]string{"parse": {}}}
	default:
		v = p
	}
	b, _ := json.Marshal(v)
	return b
}

// slackEscape는 Slack mrkdwn의 제어 문자(&, <, >)를 바꾼다. 커밋 메시지 같은 글의 <!channel>이나 <url|text>가
// 멘션·링크로 해석되지 않게 한다(https://api.slack.com/reference/surfaces/formatting#escaping).
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Result는 한 번의 전달(재시도 포함) 결과다.
type Result struct {
	Attempts   int
	StatusCode *int
	Err        error
	Duration   time.Duration
}

// ValidURL은 알림 URL이 http(s)이고 호스트가 있는지 본다(목적지 주소 검사는 보낼 때 한다).
func ValidURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}

// errRetry는 다시 시도할 만한 실패다(네트워크 오류, 5xx, 429).
type errRetry struct{ error }

// PostJSON은 body를 POST하고 2xx가 아니면 실패로 본다. 실패하면 backoff 간격으로 다시 보낸다(최대 len(backoff)+1번).
func PostJSON(ctx context.Context, client *http.Client, rawURL string, body []byte, headers map[string]string, backoff []time.Duration) Result {
	start := time.Now()
	var res Result
	for attempt := 0; ; attempt++ {
		res.Attempts = attempt + 1
		code, err := postOnce(ctx, client, rawURL, body, headers)
		if code != 0 {
			c := code
			res.StatusCode = &c
		}
		res.Err = err
		var re errRetry
		if err == nil || !errors.As(err, &re) || attempt >= len(backoff) {
			break
		}
		select {
		case <-ctx.Done():
			res.Err = ctx.Err()
			res.Duration = time.Since(start)
			return res
		case <-time.After(backoff[attempt]):
		}
	}
	if res.Err != nil {
		var re errRetry
		if errors.As(res.Err, &re) {
			res.Err = re.error
		}
	}
	res.Duration = time.Since(start)
	return res
}

// redactURL은 *url.Error에서 URL을 뺀다. Slack·Discord webhook URL은 경로에 토큰이 있어서
// 전달 기록과 로그에 남기면 안 된다.
func redactURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

func postOnce(ctx context.Context, client *http.Client, rawURL string, body []byte, headers map[string]string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return 0, redactURL(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "keel-notify/1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, ErrBlockedAddress) {
			return 0, ErrBlockedAddress // 다시 해도 같다
		}
		return 0, errRetry{redactURL(err)}
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return resp.StatusCode, nil
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return resp.StatusCode, errRetry{fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
}
