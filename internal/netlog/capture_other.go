//go:build !linux

package netlog

import "errors"

func probeCapture() error { return errors.New("network logs need Linux (AF_PACKET)") }

func openCapture(string) (capture, error) { return nil, errors.New("network logs need Linux") }
