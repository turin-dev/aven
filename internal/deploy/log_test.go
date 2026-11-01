package deploy

import "testing"

// A Manager built without a logger (tests, tools) must not panic when a background job logs.
func TestManagerLogNil(t *testing.T) {
	m := &Manager{}
	m.log().Warn("no logger configured", "case", "nil")
}
