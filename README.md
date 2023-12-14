# Teknoir CLI
This repo contain a collection of CLI tools for Teknoir.
Right now it is just a collection of bash scripts to manage different things in the platform.

## TLDR

### SSH to a Device:
```bash
ssh_device.sh -c gke_teknoir_us-central1-c_teknoir-cluster -n teknoir-retail -d orin-demo-se
```

### Port-Forward to MQTT Broker on a Device:
```bash
tunnel_device.sh -c gke_teknoir_us-central1-c_teknoir-cluster --namespace teknoir-ai --device orin-agx-64gb-se --port 31883 --to 127.0.0.1:31883
```
*Connect to the Device´s MQTT Broker on localhost:31883*

#### Port-Forward to Devstudio on a Device:
```bash
tunnel_device.sh -c gke_teknoir_us-central1-c_teknoir-cluster --namespace teknoir-ai --device orin-agx-64gb-se --port 8080 --to 127.0.0.1:31880
```
*Browse to http://localhost:8080*

### Port-Forward to an IP-Camera´s Web interface on the same network as the Device:
```bash
tunnel_device.sh -c gke_teknoir_us-central1-c_teknoir-cluster --namespace teknoir-ai --device orin-agx-64gb-se --port 8080 --to 192.168.2.137:80
```
*Browse to http://localhost:8080*

# Notes
Allows your Firebase users to authenticate in a CLI app.

## Demo

Install the demo:
```shell
$ go install github.com/nouney/firelogin/demo
```

Run it:
```shell
$ $GOPATH/bin/demo
Your browser has been opened to visit: http://localhost:8080

Authentication successfull. Welcome, <your full name>.
```

## Getting started

Install `firelogin`:
```shell
$ go get github.com/nouney/firelogin
```

Copy/paste the code below then run it:

```golang
package main

import (
	"fmt"
	"log"

	"github.com/nouney/firelogin"
)

func main() {
	flogin := firelogin.New(&firelogin.Config{
		APIKey:      "AIzaSyDzQAtfcdt8KIJJTaB1J6QOfLiqTR-W7wM",
		AuthDomain:  "auth.teknoir.dev",
	})
	// This will block until the user sign in
	user, err := flogin.Login()
	if err != nil {
		log.Panic(err)
	}
	fmt.Println("Authentication successfull! Welcome,", user.DisplayName)
}
```

It will open a [FirebaseUI](https://github.com/firebase/firebaseui-web) webpage allowing you to authenticate.

### Customization

#### FirebaseUI

```golang
package main

import (
	"fmt"
	"log"

	"github.com/nouney/firelogin"
)

func main() {
    // no providers = all
	ui := firelogin.NewFirebaseUI(
		"AppName", 
		firelogin.GITHUB_AUTH_PROVIDER_ID, 
		firelogin.GOOGLE_AUTH_PROVIDER_ID
	)
	flogin := firelogin.New(&firelogin.Config{
		APIKey:      "<YOUR FIREBASE API KEY>",
		AuthDomain:  "<YOUR FIREBASE AUTH DOMAIN>",
		URL: "https://your-domain.com/yourpage",
	})
	user, err := flogin.Login()
	if err != nil {
		log.Panic(err)
	}
	fmt.Println("Authentication successfull! Welcome,", user.DisplayName)
}
```