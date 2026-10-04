package chain

import (
	"embed"
	"encoding/json"
)

//go:embed networks.json
var presets embed.FS

// Defaults are an editable seed, never a runtime allowlist. No merchant address
// lives in source. Existing stored networks are not overwritten on upgrade.
func Defaults() Config {
	var networks []Network
	data, _ := presets.ReadFile("networks.json")
	if e := json.Unmarshal(data, &networks); e != nil {
		panic(e)
	}
	return Config{Addresses: map[string]string{"evm": "", "tron": "", "solana": ""}, Networks: networks, ToleranceBPS: 100, LifetimeMinutes: 30, ConfirmationHours: 24, AcceptLate: true}
}
