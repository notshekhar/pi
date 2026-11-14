package remote

import "github.com/notshekhar/pi/internal/core/config"

// The machines this pi has paired with — loop's remote-host-store.ts. Kept
// in auth.json next to provider credentials, under loop's key and in loop's
// shape, because each entry carries the other machine's serve token, which
// is full control of it; a host paired in loop shows up here and back.

// HostRecord is one paired machine.
type HostRecord struct {
	// ID is the host's environment id — stable for its token.
	ID string `json:"id"`
	// Label is what the host calls itself (its hostname), unless renamed.
	Label string `json:"label"`
	// URL is the base URL, no path or token: http://studio.tail1234.ts.net:5667.
	URL     string `json:"url"`
	Token   string `json:"token"`
	AddedAt int64  `json:"addedAt"`
}

const storeKey = "remoteHosts"

// loadHosts is every saved host that has the fields a connection needs.
func loadHosts() []HostRecord {
	var raw []HostRecord
	if !config.AuthStore().Get(storeKey, &raw) {
		return nil
	}
	var out []HostRecord
	for _, h := range raw {
		if h.ID != "" && h.URL != "" && h.Token != "" {
			out = append(out, h)
		}
	}
	return out
}

func saveHosts(hosts []HostRecord) error {
	if hosts == nil {
		hosts = []HostRecord{}
	}
	return config.AuthStore().Set(storeKey, hosts)
}
