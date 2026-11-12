//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"lazareplay/internal/com"
	"lazareplay/internal/dx"
	"lazareplay/internal/mf"
)

// codecs tries to start every hardware encoder and prints what happens.
func codecs() {
	list, err := dx.Adapters()
	if err != nil {
		fail(err)
	}
	for _, a := range list {
		found, err := mf.VideoEncoders(a.ID(), a.VendorID)
		if err != nil || len(found) == 0 {
			continue
		}
		fmt.Printf("adapter %s\n", a.Name)
		device, err := dx.NewDevice(a)
		if err != nil {
			fmt.Println("  device:", err)
			continue
		}
		for _, c := range found {
			fmt.Printf("  %s\n", c.Name)
			for _, line := range c.Describe() {
				fmt.Println("     ", line)
			}
			var transform unsafe.Pointer
			err := c.Activate(&transform)
			fmt.Println("      activate:", result(err))
			if err == nil {
				com.Release(&transform)
			}
			if class, ok := c.Class(); ok {
				iid := com.ID("bf94c121-5b05-4e6f-8000-ba598961414d")
				object, err := com.Create(&class, &iid)
				fmt.Println("      create by class:", result(err))
				if err == nil {
					com.Release(&object)
				}
			}
		}
		device.Close()
	}
}

func result(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}
