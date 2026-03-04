package server

import (
	"net"
	"os"
	"path/filepath"
)

func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.json"
	}
	meowPath := filepath.Join(home, ".meowclaw", "config.json")
	if _, err := os.Stat(meowPath); err == nil {
		return meowPath
	}
	legacyPath := filepath.Join(home, ".picoclaw", "config.json")
	if _, err := os.Stat(legacyPath); err == nil {
		return legacyPath
	}
	return meowPath
}

func GetLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return ""
}
