package main

import (
	hetzner "github.com/TheRideru/rancher-hcloud-driver/driver"
	"github.com/rancher/machine/libmachine/drivers/plugin"
)

func main() {
	plugin.RegisterDriver(new(hetzner.Driver))
}
