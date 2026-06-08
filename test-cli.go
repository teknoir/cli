package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"golang.org/x/oauth2"
)

const (
	issuer       = "https://auth.teknoir.cloud/auth/realms/master"
	clientID     = "teknoir-cli"
	backstageURL = "https://teknoir.cloud"
)

func main() {

	ctx := context.Background()
	conf := &oauth2.Config{
		ClientID: clientID,
		Scopes: []string{
			"openid",
			"profile",
			"email",
		},
		Endpoint: oauth2.Endpoint{
			AuthURL:       issuer + "/protocol/openid-connect/auth",
			DeviceAuthURL: issuer + "/protocol/openid-connect/auth/device",
			TokenURL:      issuer + "/protocol/openid-connect/token",
			AuthStyle:     oauth2.AuthStyleInParams,
		},
	}

	verifier := oauth2.GenerateVerifier()

	device, err := conf.DeviceAuth(
		ctx,
		oauth2.S256ChallengeOption(verifier),
	)
	if err != nil {
		log.Fatalf("start device authorization: %v", err)
	}

	if device.VerificationURIComplete != "" {
		fmt.Printf("Open this URL in your browser:\n\n%s\n\n", device.VerificationURIComplete)
	} else {
		fmt.Printf("Open this URL in your browser:\n\n%s\n\n", device.VerificationURI)
		fmt.Printf("Enter this code:\n\n%s\n\n", device.UserCode)
	}

	token, err := conf.DeviceAccessToken(
		ctx,
		device,
		oauth2.VerifierOption(verifier),
	)
	if err != nil {
		log.Fatalf("complete device authorization: %v", err)
	}
	httpClient := conf.Client(ctx, token)
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		backstageURL+"/api/catalog/entities/by-query?limit=20&fields=kind,metadata.namespace,metadata.name",
		nil,
	)
	if err != nil {
		log.Fatalf("create catalog request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	res, err := httpClient.Do(req)
	if err != nil {
		log.Fatalf("call catalog API: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		log.Fatalf("read catalog response: %v", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		log.Fatalf("catalog API failed: %s\n%s", res.Status, string(body))
	}
	fmt.Println(string(body))
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Fatalf("decode catalog response: %v", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		log.Fatalf("print catalog response: %v", err)
	}

}
