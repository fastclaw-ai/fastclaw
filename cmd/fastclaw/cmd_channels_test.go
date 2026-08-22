package main

import "testing"

func TestChannelSharedIdentityForConnect(t *testing.T) {
	if channelSharedIdentityForConnect("wecom", true) {
		t.Fatal("WeCom accepted --shared-identity")
	}
	if !channelSharedIdentityForConnect("telegram", true) {
		t.Fatal("non-WeCom channel lost --shared-identity")
	}
	if channelSharedIdentityForConnect("telegram", false) {
		t.Fatal("false --shared-identity request changed")
	}
}
