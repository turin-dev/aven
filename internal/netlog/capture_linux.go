//go:build linux

package netlog

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// probeCapture는 AF_PACKET 소켓을 열 권한(CAP_NET_RAW)이 있는지 확인한다.
func probeCapture() error {
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			return errors.New("packet capture needs CAP_NET_RAW (run aven as root or grant the capability)")
		}
		return fmt.Errorf("packet capture: %w", err)
	}
	unix.Close(fd)
	return nil
}

type packetSocket struct{ fd int }

// openCapture는 인터페이스 하나에 묶인 AF_PACKET 소켓을 연다. bridge의 다른 포트 사이 프레임도 보려고
// promiscuous membership을 건다(소켓을 닫으면 커널이 되돌린다). 읽기는 1초마다 깨어난다.
func openCapture(ifname string) (capture, error) {
	ifc, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		return nil, err
	}
	fail := func(err error) (capture, error) {
		unix.Close(fd)
		return nil, err
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_ALL), Ifindex: ifc.Index}); err != nil {
		return fail(err)
	}
	mreq := unix.PacketMreq{Ifindex: int32(ifc.Index), Type: unix.PACKET_MR_PROMISC}
	if err := unix.SetsockoptPacketMreq(fd, unix.SOL_PACKET, unix.PACKET_ADD_MEMBERSHIP, &mreq); err != nil {
		return fail(err)
	}
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 4<<20)
	tv := unix.Timeval{Sec: 1}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		return fail(err)
	}
	return &packetSocket{fd: fd}, nil
}

func (s *packetSocket) Read(buf []byte) (int, int, error) {
	// MSG_TRUNC: 버퍼보다 긴 프레임이면 원래 길이를 돌려준다(바이트 수를 맞게 세려고).
	n, _, err := unix.Recvfrom(s.fd, buf, unix.MSG_TRUNC)
	if err != nil {
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			return 0, 0, nil
		}
		return 0, 0, err // ENETDOWN 등: bridge가 사라졌다. 다음 sync에서 다시 연다
	}
	return min(n, len(buf)), n, nil
}

func (s *packetSocket) Close() error { return unix.Close(s.fd) }
