package main

import (
	"encoding/json"
	"fmt"
	"github.com/teknoir/cli/pkg/api"
)

func main() {
	jsonData := []byte(`{
		"metadata": {
			"name": "test-device"
		},
		"spec": {
			"subresources": {
				"status": {
					"config": {
						"active_config_hash": "1f06fa06de447fb3f218ed01756c5299",
						"last_config_change": "2026-06-16T06:09:11Z"
					}
				}
			}
		}
	}`)
	var device api.BackstageDevice
	err := json.Unmarshal(jsonData, &device)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	fmt.Printf("Config: %+v\n", device.Spec.Subresources.Status.Config)
}
