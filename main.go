package main

import (
	"fmt"
	"log"

	"teknoir/cli/firebaselogin"
)

func main() {
	ui := firebaselogin.NewFirebaseUI(
		"tncli",
		firebaselogin.GOOGLE_AUTH_PROVIDER_ID,
		firebaselogin.EMAIL_AUTH_PROVIDER_ID,
	)

	flogin, _ := firebaselogin.New(
		"AIzaSyDzQAtfcdt8KIJJTaB1J6QOfLiqTR-W7wM",
		"auth.teknoir.dev",
		firebaselogin.WithAuthHTML(ui.AuthHTML()),
		firebaselogin.WithSuccessHTML(ui.SuccessHTML()),
	)

	// This will block until the user signs in
	user, err := flogin.Login()
	if err != nil {
		log.Panic(err)
	}

	fmt.Println("Authentication successfull! Welcome,", user.DisplayName)
	//fmt.Println("Authentication successfull! Welcome,", user.StsTokenManager.AccessToken)
}
