//go:build windows

package main

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modiphlpapi          = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetIfEntry2      = modiphlpapi.NewProc("GetIfEntry2")
	procGetBestInterface = modiphlpapi.NewProc("GetBestInterface")
)

const (
	ifTypeSoftwareLoopback = 24
	ifTypeTunnel           = 131
	ifOperStatusUp         = 1
)

// Layout matches gopsutil / Microsoft's MIB_IF_ROW2 on 64-bit Windows.
type mibIfRow2 struct {
	InterfaceLuid               uint64
	InterfaceIndex              uint32
	InterfaceGuid               [16]byte
	Alias                       [257]uint16
	Description                 [257]uint16
	PhysicalAddressLength       uint32
	PhysicalAddress             [32]uint8
	PermanentPhysicalAddress    [32]uint8
	Mtu                         uint32
	Type                        uint32
	TunnelType                  uint32
	MediaType                   uint32
	PhysicalMediumType          uint32
	AccessType                  uint32
	DirectionType               uint32
	InterfaceAndOperStatusFlags uint32
	OperStatus                  uint32
	AdminStatus                 uint32
	MediaConnectState           uint32
	NetworkGuid                 [16]byte
	ConnectionType              uint32
	TransmitLinkSpeed           uint64
	ReceiveLinkSpeed            uint64
	InOctets                    uint64
	InUcastPkts                 uint64
	InNUcastPkts                uint64
	InDiscards                  uint64
	InErrors                    uint64
	InUnknownProtos             uint64
	InUcastOctets               uint64
	InMulticastOctets           uint64
	InBroadcastOctets           uint64
	OutOctets                   uint64
	OutUcastPkts                uint64
	OutNUcastPkts               uint64
	OutDiscards                 uint64
	OutErrors                   uint64
	OutUcastOctets              uint64
	OutMulticastOctets          uint64
	OutBroadcastOctets          uint64
	OutQLen                     uint64
}

func systemNICCounters() (in, out uint64, ok bool) {
	if idx, okIdx := bestInterfaceIndex(); okIdx {
		return ifEntryOctets(idx)
	}
	// No default route: sum up Ethernet/Wi-Fi adapters only (skip
	// loopback and tunnels so VPN + physical is not counted twice).
	ifs, err := net.Interfaces()
	if err != nil {
		return 0, 0, false
	}
	var got bool
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		rin, rout, rok := ifEntryOctets(uint32(ifi.Index))
		if !rok {
			continue
		}
		in += rin
		out += rout
		got = true
	}
	return in, out, got
}

func bestInterfaceIndex() (uint32, bool) {
	// 8.8.8.8 in network byte order as a Windows IPv4 address.
	const dns = 0x08080808
	var idx uint32
	r, _, _ := procGetBestInterface.Call(uintptr(dns), uintptr(unsafe.Pointer(&idx)))
	if r != 0 || idx == 0 {
		return 0, false
	}
	return idx, true
}

func ifEntryOctets(index uint32) (in, out uint64, ok bool) {
	if procGetIfEntry2.Find() != nil {
		return 0, 0, false
	}
	row := mibIfRow2{InterfaceIndex: index}
	r, _, _ := procGetIfEntry2.Call(uintptr(unsafe.Pointer(&row)))
	if r != 0 {
		return 0, 0, false
	}
	if row.Type == ifTypeSoftwareLoopback || row.Type == ifTypeTunnel {
		return 0, 0, false
	}
	if row.OperStatus != 0 && row.OperStatus != ifOperStatusUp {
		return 0, 0, false
	}
	return row.InOctets, row.OutOctets, true
}
