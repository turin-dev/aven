package netlog

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"strconv"
	"strings"
)

// dnsMessage는 DNS 메시지에서 기록에 필요한 부분만 읽은 것이다.
type dnsMessage struct {
	ID       uint16
	Response bool
	RCode    int
	QName    string
	QType    uint16
	Answers  []string // A/AAAA 주소, 그 밖의 타입은 "TYPE data" 요약
	CNAMEs   []string
}

var errDNS = errors.New("malformed dns message")

var qtypeNames = map[uint16]string{1: "A", 2: "NS", 5: "CNAME", 6: "SOA", 12: "PTR", 15: "MX", 16: "TXT", 28: "AAAA",
	33: "SRV", 64: "SVCB", 65: "HTTPS", 255: "ANY"}

var rcodeNames = map[int]string{0: "NOERROR", 1: "FORMERR", 2: "SERVFAIL", 3: "NXDOMAIN", 4: "NOTIMP", 5: "REFUSED"}

// QTypeName은 질의 타입 이름(모르면 TYPE<n>)이다.
func QTypeName(t uint16) string {
	if n, ok := qtypeNames[t]; ok {
		return n
	}
	return "TYPE" + strconv.Itoa(int(t))
}

// RCodeName은 응답 코드 이름이다.
func RCodeName(c int) string {
	if n, ok := rcodeNames[c]; ok {
		return n
	}
	return "RCODE" + strconv.Itoa(c)
}

// parseDNS는 질문 하나와 answer section을 읽는다. 압축 포인터를 따라가되 고리와 과도한 길이를 막는다.
func parseDNS(b []byte) (dnsMessage, error) {
	var m dnsMessage
	if len(b) < 12 {
		return m, errDNS
	}
	m.ID = binary.BigEndian.Uint16(b[0:2])
	flags := binary.BigEndian.Uint16(b[2:4])
	m.Response = flags&0x8000 != 0
	if op := (flags >> 11) & 0xf; op != 0 {
		return m, errDNS // 표준 질의만
	}
	m.RCode = int(flags & 0xf)
	qd, an := binary.BigEndian.Uint16(b[4:6]), binary.BigEndian.Uint16(b[6:8])
	if qd != 1 {
		return m, errDNS
	}
	off := 12
	name, next, err := readName(b, off)
	if err != nil || next+4 > len(b) {
		return m, errDNS
	}
	m.QName, m.QType = name, binary.BigEndian.Uint16(b[next:next+2])
	off = next + 4
	for i := 0; i < int(an) && i < 64; i++ {
		_, next, err := readName(b, off)
		if err != nil || next+10 > len(b) {
			break
		}
		typ := binary.BigEndian.Uint16(b[next : next+2])
		rdlen := int(binary.BigEndian.Uint16(b[next+8 : next+10]))
		rd := next + 10
		if rd+rdlen > len(b) {
			break
		}
		data := b[rd : rd+rdlen]
		switch typ {
		case 1:
			if len(data) == 4 {
				m.Answers = append(m.Answers, netip.AddrFrom4([4]byte(data)).String())
			}
		case 28:
			if len(data) == 16 {
				m.Answers = append(m.Answers, netip.AddrFrom16([16]byte(data)).String())
			}
		case 5:
			if target, _, err := readName(b, rd); err == nil {
				m.CNAMEs = append(m.CNAMEs, target)
			}
		case 12, 2:
			if target, _, err := readName(b, rd); err == nil {
				m.Answers = append(m.Answers, target)
			}
		default:
			m.Answers = append(m.Answers, QTypeName(typ))
		}
		off = rd + rdlen
	}
	return m, nil
}

// readName은 off에서 이름을 읽고 (압축 포인터 뒤가 아닌) 다음 위치를 돌려준다.
func readName(b []byte, off int) (string, int, error) {
	var labels []string
	next := -1
	total := 0
	for jumps := 0; ; {
		if off >= len(b) {
			return "", 0, errDNS
		}
		l := int(b[off])
		switch {
		case l == 0:
			if next < 0 {
				next = off + 1
			}
			name := strings.ToLower(strings.Join(labels, "."))
			if name == "" {
				name = "."
			}
			return name, next, nil
		case l&0xc0 == 0xc0:
			if off+1 >= len(b) {
				return "", 0, errDNS
			}
			if jumps++; jumps > 16 {
				return "", 0, errDNS
			}
			if next < 0 {
				next = off + 2
			}
			off = int(binary.BigEndian.Uint16(b[off:off+2]) & 0x3fff)
		case l&0xc0 != 0:
			return "", 0, errDNS
		default:
			if off+1+l > len(b) {
				return "", 0, errDNS
			}
			if total += l + 1; total > 255 {
				return "", 0, errDNS
			}
			labels = append(labels, sanitizeLabel(b[off+1:off+1+l]))
			off += 1 + l
		}
	}
}

// sanitizeLabel은 출력할 수 없는 바이트를 \DDD로 바꾼다(로그와 터미널을 깨지 않게).
func sanitizeLabel(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c > 0x20 && c < 0x7f && c != '.' && c != '\\' {
			sb.WriteByte(c)
		} else {
			sb.WriteString("\\" + strconv.Itoa(int(c)))
		}
	}
	return sb.String()
}

// Zone은 이름이 사설 네트워크 쪽(internal)인지 외부(external)인지 정한다.
func Zone(qname string) string {
	n := strings.TrimSuffix(strings.ToLower(qname), ".")
	if n == "" || !strings.Contains(n, ".") || n == "internal" || strings.HasSuffix(n, ".internal") || strings.HasSuffix(n, ".local") {
		return "internal"
	}
	return "external"
}
