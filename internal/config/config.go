package config

import "os"

type Config struct {
	BindAddr       string // "127.0.0.1" — never bind the LAN directly
	Port           string // "8473"
	DataDir        string // "/DATA/AppData/zima-vm-extras"
	StaticDir      string // "/usr/share/casaos/www/modules/zima_vm_extras"
	VirshBin       string // "/usr/bin/virsh"
	RoutePath      string // gateway route prefix, "/v2/vm_extras"
	GatewayURLFile string // "/var/run/casaos/management.url"
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func Load() Config {
	return Config{
		BindAddr:       env("BIND_ADDR", "127.0.0.1"),
		Port:           env("PORT", "8473"),
		DataDir:        env("DATA_DIR", "/DATA/AppData/zima-vm-extras"),
		StaticDir:      env("STATIC_DIR", "/usr/share/casaos/www/modules/zima_vm_extras"),
		VirshBin:       env("VIRSH_BIN", "/usr/bin/virsh"),
		RoutePath:      env("ROUTE_PATH", "/v2/vm_extras"),
		GatewayURLFile: env("GATEWAY_URL_FILE", "/var/run/casaos/management.url"),
	}
}
