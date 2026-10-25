package events

import (
	"encoding/json"
	"testing"
)

func TestPayloadHasDiscordAndSlackKeys(t *testing.T) {
	var m map[string]string
	if err := json.Unmarshal(Payload("example.com", "deploy.failed", "web 배포 실패"), &m); err != nil {
		t.Fatal(err)
	}
	if m["content"] == "" || m["content"] != m["text"] {
		t.Errorf("payload: %v", m)
	}
}
