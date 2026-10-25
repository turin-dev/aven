package netlog

import (
	"encoding/binary"
	"net/netip"
)

const (
	protoTCP = 6
	protoUDP = 17

	tcpFIN = 0x01
	tcpSYN = 0x02
	tcpRST = 0x04
	tcpACK = 0x10
)

// Packet은 Ethernet 프레임에서 읽은 L3/L4 헤더다.
type Packet struct {
	Src, Dst         netip.Addr
	SrcPort, DstPort uint16
	Proto            uint8
	Flags            uint8 // TCP flags
	Length           int   // IP 패킷 길이(헤더 포함)
	Payload          []byte
}

// ParseFrame은 Ethernet(II, 802.1Q 하나까지) 프레임에서 IPv4/IPv6 위의 TCP/UDP 패킷을 읽는다.
// frameLen은 잘리기 전의 원래 길이다(버퍼보다 큰 프레임도 바이트 수를 맞게 센다).
func ParseFrame(b []byte, frameLen int) (Packet, bool) {
	if len(b) < 14 {
		return Packet{}, false
	}
	et := binary.BigEndian.Uint16(b[12:14])
	off := 14
	if et == 0x8100 && len(b) >= 18 {
		et = binary.BigEndian.Uint16(b[16:18])
		off = 18
	}
	if frameLen < len(b) {
		frameLen = len(b)
	}
	switch et {
	case 0x0800:
		return parseIPv4(b[off:], frameLen-off)
	case 0x86dd:
		return parseIPv6(b[off:], frameLen-off)
	}
	return Packet{}, false
}

func parseIPv4(b []byte, wire int) (Packet, bool) {
	if len(b) < 20 || b[0]>>4 != 4 {
		return Packet{}, false
	}
	ihl := int(b[0]&0x0f) * 4
	if ihl < 20 || len(b) < ihl {
		return Packet{}, false
	}
	// 조각난 패킷의 뒤 조각에는 L4 헤더가 없다.
	if binary.BigEndian.Uint16(b[6:8])&0x1fff != 0 {
		return Packet{}, false
	}
	p := Packet{Proto: b[9], Src: netip.AddrFrom4([4]byte(b[12:16])), Dst: netip.AddrFrom4([4]byte(b[16:20]))}
	p.Length = int(binary.BigEndian.Uint16(b[2:4]))
	if p.Length < wire {
		p.Length = wire // GRO/GSO로 합쳐진 패킷
	}
	end := min(len(b), int(binary.BigEndian.Uint16(b[2:4])))
	if end < ihl {
		end = len(b)
	}
	return parseL4(p, b[ihl:end])
}

func parseIPv6(b []byte, wire int) (Packet, bool) {
	if len(b) < 40 || b[0]>>4 != 6 {
		return Packet{}, false
	}
	p := Packet{Proto: b[6], Src: netip.AddrFrom16([16]byte(b[8:24])), Dst: netip.AddrFrom16([16]byte(b[24:40]))}
	plen := int(binary.BigEndian.Uint16(b[4:6]))
	p.Length = 40 + plen
	if p.Length < wire {
		p.Length = wire
	}
	end := len(b)
	if plen > 0 && 40+plen < end {
		end = 40 + plen
	}
	rest := b[40:end]
	// 흔한 확장 헤더(hop-by-hop, routing, destination options)는 건너뛴다. fragment(44)는 버린다.
	for i := 0; i < 4 && (p.Proto == 0 || p.Proto == 43 || p.Proto == 60); i++ {
		if len(rest) < 8 {
			return Packet{}, false
		}
		n := (int(rest[1]) + 1) * 8
		if len(rest) < n {
			return Packet{}, false
		}
		p.Proto, rest = rest[0], rest[n:]
	}
	return parseL4(p, rest)
}

func parseL4(p Packet, b []byte) (Packet, bool) {
	switch p.Proto {
	case protoTCP:
		if len(b) < 20 {
			return Packet{}, false
		}
		p.SrcPort, p.DstPort = binary.BigEndian.Uint16(b[0:2]), binary.BigEndian.Uint16(b[2:4])
		p.Flags = b[13]
		if doff := int(b[12]>>4) * 4; doff >= 20 && doff <= len(b) {
			p.Payload = b[doff:]
		}
		return p, true
	case protoUDP:
		if len(b) < 8 {
			return Packet{}, false
		}
		p.SrcPort, p.DstPort = binary.BigEndian.Uint16(b[0:2]), binary.BigEndian.Uint16(b[2:4])
		end := len(b)
		if ul := int(binary.BigEndian.Uint16(b[4:6])); ul >= 8 && ul < end {
			end = ul
		}
		p.Payload = b[8:end]
		return p, true
	}
	return Packet{}, false
}
