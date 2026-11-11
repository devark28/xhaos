//go:build !linux

package main

import "errors"

func applyUSB([]string) error { return errors.New("apply-usb is for Linux nodes") }
