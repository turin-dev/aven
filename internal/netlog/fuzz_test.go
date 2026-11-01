package netlog

import "testing"

// 캡처한 프레임과 DNS 메시지는 네트워크의 아무 컨테이너나 만든다. 어떤 바이트가 와도 panic하지 않아야 한다.

func FuzzParseFrame(f *testing.F) {
	f.Add([]byte{}, 0)
	f.Add(make([]byte, 14), 14)
	f.Add(append([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x08, 0x00, 0x45}, make([]byte, 40)...), 64)
	f.Add(append([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x86, 0xdd, 0x60}, make([]byte, 60)...), 80)
	f.Fuzz(func(t *testing.T, b []byte, wire int) {
		ParseFrame(b, wire)
	})
}

func FuzzParseDNS(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 1, 0x81, 0x80, 0, 1, 0, 1, 0, 0, 0, 0, 1, 'a', 0, 0, 1, 0, 1, 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4, 1, 2, 3, 4})
	f.Add([]byte{0, 1, 0x81, 0x80, 0, 1, 0, 1, 0, 0, 0, 0, 0xc0, 12})
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := parseDNS(b)
		if err == nil && len(m.QName) > 4*255 {
			t.Fatalf("qname too long: %d", len(m.QName))
		}
	})
}
