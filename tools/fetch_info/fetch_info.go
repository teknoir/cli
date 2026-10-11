package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Println("Error loading config:", err)
		return
	}
	
	domain := cfg.Domain
	namespace := cfg.GetNamespace()
	
	fmt.Printf("Fetching device for %s/%s\n", domain, namespace)
	device, err := api.FetchDeviceDetails(context.Background(), domain, namespace, "rtx2000-ada-64gb-se")
	if err != nil {
		fmt.Println("Error fetching:", err)
		return
	}
	
	// Print raw JSON for the whole spec to see where config is
	b, _ := json.MarshalIndent(device.Spec.Subresources.Status.Config, "", "  ")
	fmt.Printf("Config JSON:\n%s\n", string(b))
}
