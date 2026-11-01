package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func samplePayload(prefix string) string {
	row := "test-host,1.2.3.4,1000,10,100000000,Japan,JP,5,864000000,100,123456,2weeks,Operator,,"
	row += base64.StdEncoding.EncodeToString([]byte("remote 1.2.3.4 443\n"))
	return prefix + "*vpn_servers\n#HostName,IP,Score,Ping,Speed,CountryLong,CountryShort,NumVpnSessions,Uptime,TotalUsers,TotalTraffic,LogType,Operator,Message,OpenVPN_ConfigData_Base64\n" + row + "\n*\n"
}

func TestParseOfficial(t *testing.T) {
	s, err := parseServers(samplePayload(""))
	if err != nil || len(s) != 1 {
		t.Fatalf("official: %v %d", err, len(s))
	}
	if s[0].UptimeDays != 10 || s[0].SpeedMbps != 100 {
		t.Fatalf("fields: %+v", s[0])
	}
}

func TestParseMirrorPrefixed(t *testing.T) {
	prefix := "Title: \n\nURL Source: http://www.vpngate.net/api/iphone/\n\nMarkdown Content:\n"
	s, err := parseServers(samplePayload(prefix))
	if err != nil || len(s) != 1 {
		t.Fatalf("mirror: %v %d", err, len(s))
	}
	if !strings.HasPrefix(s[0].HostName, "test-") {
		t.Fatalf("host: %s", s[0].HostName)
	}
}
