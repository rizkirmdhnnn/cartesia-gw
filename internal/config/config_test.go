package config

import "testing"

func TestIsLoopback(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{addr: "127.0.0.1:20140", want: true},
		{addr: "localhost:20140", want: true},
		{addr: "[::1]:20140", want: true},
		{addr: "0.0.0.0:20140", want: false},
		{addr: ":20140", want: false},
		{addr: "[::]:20140", want: false},
		{addr: "192.168.1.10:20140", want: false},
		{addr: "example.test:20140", want: false},
	}
	for _, tt := range tests {
		if got := IsLoopback(tt.addr); got != tt.want {
			t.Errorf("IsLoopback(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}

// Binding to the network without a credential would expose a dashboard that
// lists Cartesia API keys, so the combination is refused at startup.
func TestValidateRefusesNetworkBindingWithoutAToken(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "loopback without a token is fine",
			cfg:  Config{Addr: "127.0.0.1:20140", DBPath: "/tmp/db", CartesiaVersion: DefaultCartesiaVersion, ElevenLabsBaseURL: DefaultElevenLabsBaseURL},
		},
		{
			name:    "a public bind without a token is refused",
			cfg:     Config{Addr: "0.0.0.0:20140", DBPath: "/tmp/db", CartesiaVersion: DefaultCartesiaVersion, ElevenLabsBaseURL: DefaultElevenLabsBaseURL},
			wantErr: true,
		},
		{
			name: "a public bind with a token is allowed",
			cfg:  Config{Addr: "0.0.0.0:20140", GatewayToken: "t", DBPath: "/tmp/db", CartesiaVersion: DefaultCartesiaVersion, ElevenLabsBaseURL: DefaultElevenLabsBaseURL},
		},
		{
			name:    "an empty version header is refused",
			cfg:     Config{Addr: "127.0.0.1:20140", DBPath: "/tmp/db"},
			wantErr: true,
		},
		{
			name:    "a malformed address is refused",
			cfg:     Config{Addr: "not-a-host:port:extra", DBPath: "/tmp/db", CartesiaVersion: DefaultCartesiaVersion, ElevenLabsBaseURL: DefaultElevenLabsBaseURL},
			wantErr: true,
		},
		{
			name:    "an empty db path is refused",
			cfg:     Config{Addr: "127.0.0.1:20140", CartesiaVersion: DefaultCartesiaVersion, ElevenLabsBaseURL: DefaultElevenLabsBaseURL},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
