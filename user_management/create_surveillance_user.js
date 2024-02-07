const {google} = require('googleapis');
const identitytoolkit = google.identitytoolkit('v3');
var firebaseAdmin = require('firebase-admin');
var app = firebaseAdmin.initializeApp({
    credential: firebaseAdmin.credential.applicationDefault()
}).auth()

const email = process.argv[2];
const fullName = process.argv[3];
const password = process.argv[4];
const namespace = process.argv[5];

console.log('email', email);
console.log('fullName', fullName);
console.log('password', password);
console.log('namespace', namespace);

const createUser = () => {
    app
        .createUser({
            email: email,
            emailVerified: false,
            phoneNumber: '+11234567890',
            password: password,
            displayName: fullName,
            photoURL: 'http://www.example.com/12345678/photo.png',
            disabled: false,
        })
        .then((userRecord) => {
            // See the UserRecord reference doc for the contents of userRecord.
            console.log('Successfully created new user:', userRecord.uid);
            if (userRecord.toJSON()['email'] == email) {
                let user = userRecord.toJSON()
                user['customClaims'] = {
                    teknoir: {
                        role: "viewer",
                        owner: [],
                        editor: [],
                        viewer: [namespace]
                    }
                }
                console.log(JSON.stringify(user));

                app.setCustomUserClaims(user["uid"] , user['customClaims']).then(() => {
                    console.log("done!")
                });
            }
        })
        .catch((error) => {
            console.log('Error creating new user:', error);
        });
}
createUser();