package network

import (
	"context"
	"testing"
)

func TestSplitSubnets(t *testing.T) {
	cases := []struct {
		name    string
		subnets string
		wantLen int
	}{
		{"single", "192.168.0.0/24", 1},
		{"multiple", "192.168.0.0/24,10.0.0.0/8", 2},
		{"with spaces", " 192.168.0.0/24 , 10.0.0.0/8 ", 2},
		{"empty", "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitSubnets(tc.subnets)
			if len(got) != tc.wantLen {
				t.Errorf("splitSubnets(%q) = %d elements, want %d", tc.subnets, len(got), tc.wantLen)
			}
		})
	}
}

func TestIsValidSubnet(t *testing.T) {
	cases := []struct {
		name    string
		subnet  string
		wantErr bool
	}{
		{"valid", "192.168.0.0/24", false},
		{"valid large", "10.0.0.0/8", false},
		{"valid host", "192.168.1.1/32", false},
		{"invalid format", "not-a-subnet", true},
		{"missing cidr", "192.168.0.0", true},
		{"negative bits", "10.0.0.0/-1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := isValidSubnet(tc.subnet)
			if tc.wantErr && err == nil {
				t.Errorf("isValidSubnet(%q) expected error", tc.subnet)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("isValidSubnet(%q) unexpected error: %v", tc.subnet, err)
			}
		})
	}
}

func TestIsValidSubnets(t *testing.T) {
	cases := []struct {
		name    string
		subnets string
		wantErr bool
	}{
		{"single valid", "192.168.0.0/24", false},
		{"multiple valid", "192.168.0.0/24,10.0.0.0/8", false},
		{"one invalid", "192.168.0.0/24,bad-subnet", true},
		{"all invalid", "bad1,bad2", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := IsValidSubnets(tc.subnets)
			if tc.wantErr && err == nil {
				t.Errorf("IsValidSubnets(%q) expected error", tc.subnets)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("IsValidSubnets(%q) unexpected error: %v", tc.subnets, err)
			}
		})
	}
}

func TestIsIpInSubnets(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		ip      string
		subnets string
		want    bool
	}{
		{"ip in subnet", "192.168.0.5", "192.168.0.0/24", true},
		{"ip not in subnet", "10.0.0.5", "192.168.0.0/24", false},
		{"multiple subnets, in first", "192.168.0.5", "192.168.0.0/24,10.0.0.0/8", true},
		{"multiple subnets, in second", "10.1.2.3", "192.168.0.0/24,10.0.0.0/8", true},
		{"multiple subnets, in neither", "172.16.0.1", "192.168.0.0/24,10.0.0.0/8", false},
		{"invalid subnet returns false", "192.168.0.5", "not-a-subnet", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsIpInSubnets(ctx, tc.ip, tc.subnets); got != tc.want {
				t.Errorf("IsIpInSubnets(%q, %q) = %v, want %v", tc.ip, tc.subnets, got, tc.want)
			}
		})
	}
}
