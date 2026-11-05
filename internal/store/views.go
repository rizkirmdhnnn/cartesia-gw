package store

// KeyView is one upstream key plus what the dashboard needs to show where
// traffic goes. The secret is never populated here.
type KeyView struct {
	ID            int64  `json:"id"`
	Label         string `json:"label"`
	Provider      string `json:"provider"`
	MaskedSecret  string `json:"maskedSecret"`
	IsActive      bool   `json:"isActive"`
	Priority      int    `json:"priority"`
	Strikes       int    `json:"strikes"`
	CooldownUntil int64  `json:"cooldownUntil"`
	Cooling       bool   `json:"cooling"`
	LastError     string `json:"lastError"`
	LastUsedAt    int64  `json:"lastUsedAt"`
	CreatedAt     int64  `json:"createdAt"`
	// Requests24h counts the requests that actually used this key, Share is
	// its slice of the provider's traffic, and Eligible says whether the
	// picker would consider it right now.
	Requests24h int64   `json:"requests24h"`
	Share       float64 `json:"share"`
	Eligible    bool    `json:"eligible"`
	// Success24h, Chars24h and Latency24h are the same window's service
	// quality.
	Success24h int64 `json:"success24h"`
	Chars24h   int64 `json:"chars24h"`
	Latency24h int64 `json:"avgLatencyMs24h"`
	// NextUp is true for the key the current strategy would pick next for its
	// provider. It is a live preview from the same query the picker runs,
	// without touching last_used.
	NextUp bool `json:"nextUp"`
}
