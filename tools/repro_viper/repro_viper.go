package main

import (
	"fmt"
	"github.com/spf13/viper"
	"os"
)

func main() {
	viper.SetConfigType("yaml")
	
	// Test case: no dot in key
	domain := "teknoir.cloud"
	clientSecret := "secret123"
	
	// Proposed fix:
	auths := make(map[string]map[string]string)
	auths[domain] = map[string]string{"client_secret": clientSecret}
	viper.Set("auths", auths)
	
	// Print the result to see the structure
	viper.WriteConfigAs("repro.yaml")
	b, _ := os.ReadFile("repro.yaml")
	fmt.Println(string(b))
	
	os.Remove("repro.yaml")
}
