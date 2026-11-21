package discovery

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	i18npkg "webhola/native/internal/i18n"
)

var getTCPTable = syscall.NewLazyDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

var QueryProcessName = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")

var enumProcessModules = syscall.NewLazyDLL("psapi.dll").NewProc("EnumProcessModulesEx")

var moduleBaseName = syscall.NewLazyDLL("psapi.dll").NewProc("GetModuleBaseNameW")

// Also recognizes service wrappers that load jvm.dll under a non-Java name.
func processIsJVM(pid uint32, name string) bool {
	if strings.EqualFold(name, "java.exe") || strings.EqualFold(name, "javaw.exe") {
		return true
	}
	h, err := syscall.OpenProcess(0x0400|0x0010, false, pid)
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	modules := make([]uintptr, 2048)
	var needed uint32
	ok, _, _ := enumProcessModules.Call(uintptr(h), uintptr(unsafe.Pointer(&modules[0])), uintptr(len(modules))*unsafe.Sizeof(modules[0]), uintptr(unsafe.Pointer(&needed)), 3)
	if ok == 0 {
		return false
	}
	count := min(int(uintptr(needed)/unsafe.Sizeof(modules[0])), len(modules))
	for _, module := range modules[:count] {
		var buf [260]uint16
		n, _, _ := moduleBaseName.Call(uintptr(h), module, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n > 0 && strings.EqualFold(syscall.UTF16ToString(buf[:]), "jvm.dll") {
			return true
		}
	}
	return false
}

func tcpTable(family uint32) ([]byte, error) {
	var size uint32
	for attempt := 0; attempt < 4; attempt++ {
		var b []byte
		var ptr uintptr
		if size > 0 {
			b = make([]byte, size)
			ptr = uintptr(unsafe.Pointer(&b[0]))
		}
		// TCP_TABLE_OWNER_PID_LISTENER = 3. Only listeners, no connection history.
		code, _, _ := getTCPTable.Call(ptr, uintptr(unsafe.Pointer(&size)), 1, uintptr(family), 3, 0)
		if code == 122 {
			continue
		}
		if code != 0 {
			return nil, syscall.Errno(code)
		}
		return b, nil
	}
	return nil, i18npkg.MessageError("service_scan_windows.tcp_listener_table_changed_while_reading_scan")
}

func processName(pid uint32) string {
	handle, err := syscall.OpenProcess(0x1000, false, pid)
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(handle)
	var buf [1024]uint16
	size := uint32(len(buf))
	ok, _, _ := QueryProcessName.Call(uintptr(handle), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return ""
	}
	return filepath.Base(syscall.UTF16ToString(buf[:size]))
}

func DecodeListeners(b []byte, ipv6 bool) ([]Listener, error) {
	if len(b) < 4 {
		return nil, i18npkg.MessageError("service_scan_windows.tcp_listener_table_is_empty_or_corrupt")
	}
	count := int(binary.LittleEndian.Uint32(b))
	stride := 24
	if ipv6 {
		stride = 56
	}
	if count > (len(b)-4)/stride {
		return nil, i18npkg.MessageError("service_scan_windows.tcp_listener_table_is_incomplete")
	}
	var out []Listener
	for i := 0; i < count; i++ {
		r := b[4+i*stride : 4+(i+1)*stride]
		var l Listener
		if ipv6 {
			l.IP = net.IP(r[:16]).String()
			scope := binary.LittleEndian.Uint32(r[16:20])
			if scope != 0 {
				l.IP += fmt.Sprintf("%%%d", scope)
			}
			l.Port = int(binary.BigEndian.Uint16(r[20:22]))
			l.PID = binary.LittleEndian.Uint32(r[52:56])
		} else {
			l.IP = net.IP(r[4:8]).String()
			l.Port = int(binary.BigEndian.Uint16(r[8:10]))
			l.PID = binary.LittleEndian.Uint32(r[20:24])
		}
		if l.Port > 0 {
			out = append(out, l)
		}
	}
	return out, nil
}

func ListListeners(ctx context.Context) ([]Listener, error) {
	var all []Listener
	var firstErr error
	success := false
	names := map[uint32]string{}
	jvms := map[uint32]bool{}
	for _, af := range []uint32{syscall.AF_INET, syscall.AF_INET6} {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		b, err := tcpTable(af)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		list, err := DecodeListeners(b, af == syscall.AF_INET6)
		if err != nil {
			return nil, err
		}
		all = append(all, list...)
		success = true
	}
	if !success {
		return nil, firstErr
	}
	for i := range all {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		pid := all[i].PID
		name, ok := names[pid]
		if !ok {
			name = processName(pid)
			names[pid] = name
			jvms[pid] = processIsJVM(pid, name)
		}
		all[i].Process = name
		all[i].JVM = jvms[pid]
	}
	return all, nil
}

func LocalAdapters() ([]LocalAdapter, error) {
	guids := map[int]string{}
	var size uint32
	if err := syscall.GetAdaptersInfo(nil, &size); err == syscall.ERROR_BUFFER_OVERFLOW && size > 0 {
		buf := make([]byte, size)
		head := (*syscall.IpAdapterInfo)(unsafe.Pointer(&buf[0]))
		if syscall.GetAdaptersInfo(head, &size) == nil {
			for a := head; a != nil; a = a.Next {
				guid := strings.TrimRight(string(a.AdapterName[:]), "\x00")
				guids[int(a.Index)] = `\Device\NPF_` + guid
			}
		}
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var result []LocalAdapter
	for _, n := range interfaces {
		a := LocalAdapter{Name: n.Name, CaptureName: guids[n.Index]}
		addrs, err := n.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil {
				a.Addresses = append(a.Addresses, ip)
			}
		}
		result = append(result, a)
	}
	return result, nil
}
