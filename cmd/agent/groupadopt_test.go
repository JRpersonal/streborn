package main

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

func TestToFirmwareGroupKeepsEveryField(t *testing.T) {
	fg := toFirmwareGroup(boxapi.Group{
		ID: "g1", Name: "Wohnzimmer", MasterDeviceID: "AAA", Status: "GROUP_OK",
		Members: []boxapi.ZoneMember{
			{DeviceID: "AAA", Role: "LEFT", IP: "192.0.2.10"},
			{DeviceID: "BBB", Role: "RIGHT", IP: "192.0.2.11"},
		},
	})
	if fg.ID != "g1" || fg.Name != "Wohnzimmer" || fg.MasterDeviceID != "AAA" || fg.Status != "GROUP_OK" {
		t.Fatalf("group fields lost: %+v", fg)
	}
	if len(fg.Roles) != 2 || fg.Roles[1].DeviceID != "BBB" || fg.Roles[1].Role != "RIGHT" || fg.Roles[1].IP != "192.0.2.11" {
		t.Fatalf("roles lost: %+v", fg.Roles)
	}
}

func TestFirmwareGroupProbeNeedsAHost(t *testing.T) {
	if firmwareGroupProbe("") != nil {
		t.Fatal("no box host must mean no probe")
	}
}
