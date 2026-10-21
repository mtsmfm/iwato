package main

import (
	"log"

	"golang.org/x/sys/windows/registry"
)

// hostZoneName is the IANA name of Windows' time zone ("" when unknown).
func hostZoneName() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\TimeZoneInformation`, registry.QUERY_VALUE)
	if err != nil {
		log.Printf("time zone: %v", err)
		return ""
	}
	defer k.Close()
	name, _, err := k.GetStringValue("TimeZoneKeyName")
	if err != nil {
		log.Printf("time zone: %v", err)
		return ""
	}
	z, ok := windowsZone(name)
	if !ok {
		log.Printf("time zone: unknown Windows zone %q, using UTC", name)
		return ""
	}
	return z
}
