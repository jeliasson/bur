package ports

import "testing"

func TestParseSpec(t *testing.T) {
	ip, h, c, err := ParseSpec("8000")
	if err != nil || ip != "" || h != 8000 || c != 8000 {
		t.Errorf("plain port: %q %d %d %v", ip, h, c, err)
	}
	ip, h, c, err = ParseSpec("8080:80")
	if err != nil || ip != "" || h != 8080 || c != 80 {
		t.Errorf("pair: %q %d %d %v", ip, h, c, err)
	}
	ip, h, c, err = ParseSpec("0.0.0.0:8080:80")
	if err != nil || ip != "0.0.0.0" || h != 8080 || c != 80 {
		t.Errorf("host ip: %q %d %d %v", ip, h, c, err)
	}
	if _, _, _, err := ParseSpec("x"); err == nil {
		t.Error("garbage should error")
	}
	if _, _, _, err := ParseSpec("notanip:8080:80"); err == nil {
		t.Error("invalid host ip should error")
	}
}

func TestMappingBindIP(t *testing.T) {
	if got := (Mapping{Host: 8000, Container: 8000}).BindIP(); got != DefaultPublishIP {
		t.Errorf("default bind ip = %q, want %q", got, DefaultPublishIP)
	}
	if got := (Mapping{HostIP: "0.0.0.0", Host: 8000, Container: 8000}).BindIP(); got != "0.0.0.0" {
		t.Errorf("explicit bind ip = %q, want 0.0.0.0", got)
	}
}
