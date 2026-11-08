package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/agentprotocol"
)

// A panic inside one operation (here: a Handler without a Docker client) is returned as that
// operation's error instead of crashing the agent.
func TestHandleRecoveringTurnsPanicIntoError(t *testing.T) {
	h := &Handler{}
	payload, _ := json.Marshal(agentprotocol.TCPRemoveRequest{InstanceID: "ins_abcdefgh"})
	res, err := h.handleRecovering(context.Background(), agentprotocol.OpTCPRemove, payload, func(any) {})
	if err == nil || !strings.Contains(err.Error(), "internal error") || res != nil {
		t.Fatalf("got %v, %v; want an internal-error result", res, err)
	}
}
